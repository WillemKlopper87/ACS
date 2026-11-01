// Package secretstore holds short-lived sealed secrets behind single-use
// handles (I4). A BSS stores a secret, such as a Wi-Fi passphrase, and gets
// a handle; an order names the handle instead of the value and claims it
// for one device; the CWMP gateway opens it when it sends the write, which
// deletes it. Expired handles are swept. The value is never in an order,
// a job payload, an audit record, an event or a log: only the handle,
// which is not secret.
//
// Values are sealed with AES-256-GCM under a key derived from
// ACS_BSS_SECRET_KEY, which is separate from the database credentials, with
// the handle and its owner as additional data so a sealed value cannot be
// moved to another row.
package secretstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// KeyEnv names the key's environment variable; cmd/bssadapter (sealing)
// and cmd/acs (opening) both read it.
const KeyEnv = "ACS_BSS_SECRET_KEY"

// MinKeyLength is the shortest key accepted: `openssl rand -base64 32`
// gives 44 characters.
const MinKeyLength = 32

// MaxValueLength bounds a stored value (a WPA passphrase is at most 63
// characters; 256 leaves room for other secrets without making the store
// a blob store).
const MaxValueLength = 256

var (
	// ErrDisabled: no key is configured, so handles cannot be made or
	// opened.
	ErrDisabled = errors.New("secret store disabled: " + KeyEnv + " is not set")
	// ErrInvalidHandle: the handle is unknown, expired, already claimed
	// or owned by another customer. One error for all of them, so a
	// handle never confirms that another customer has it.
	ErrInvalidHandle = errors.New("secret handle unknown, expired or already used")
	// ErrHandleGone: at dispatch the handle was already used (a retried
	// job) or has expired. The BSS stores the secret again and sends a
	// new order.
	ErrHandleGone = errors.New("secret handle already used or expired")
)

// Store is the bss_secrets table plus the sealing key. A Store with no key
// answers ErrDisabled.
type Store struct {
	db   *sql.DB
	aead cipher.AEAD
}

// New builds a Store. An empty key disables it; a key shorter than
// MinKeyLength is refused, since it would be the only protection of the
// values at rest.
func New(db *sql.DB, key string) (*Store, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return &Store{db: db}, nil
	}
	if len(key) < MinKeyLength {
		return nil, fmt.Errorf("%s must be at least %d characters", KeyEnv, MinKeyLength)
	}
	// A domain-separated hash, so the same string used for another key
	// (it should not be) still yields a different AES key.
	sum := sha256.Sum256([]byte("acs-bss-secrets-v1\x00" + key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, aead: aead}, nil
}

// Enabled reports whether a key is configured.
func (s *Store) Enabled() bool { return s != nil && s.aead != nil }

// Handle is a stored secret's public identity.
type Handle struct {
	ID        string    `json:"handle"`
	ExpiresAt time.Time `json:"expires_at"`
}

func additionalData(id, owner string) []byte { return []byte(id + "\x00" + owner) }

// Put seals value for owner (a customer id; empty for a global client) and
// returns its handle, valid for ttl.
func (s *Store) Put(ctx context.Context, owner, value string, ttl time.Duration) (*Handle, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	id := "sh_" + base64.RawURLEncoding.EncodeToString(raw)
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(value), additionalData(id, owner))
	var expires time.Time
	err := s.db.QueryRowContext(ctx, `INSERT INTO bss_secrets (id, customer_id, sealed, expires_at)
		VALUES ($1, NULLIF($2, '')::uuid, $3, now() + make_interval(secs => $4::float8)) RETURNING expires_at`,
		id, owner, sealed, ttl.Seconds()).Scan(&expires)
	if err != nil {
		return nil, fmt.Errorf("store secret: %w", err)
	}
	return &Handle{ID: id, ExpiresAt: expires.UTC()}, nil
}

// Check reports whether owner may use an unclaimed, unexpired handle,
// without claiming it (preflight).
func (s *Store) Check(ctx context.Context, id, owner string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM bss_secrets WHERE id = $1 AND customer_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid
		AND device_id IS NULL AND expires_at > now()`, id, owner).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidHandle
	}
	return err
}

// Claim binds an unclaimed, unexpired handle of owner to the device an
// order writes to. A handle is claimed once: a second order naming it is
// refused.
func (s *Store) Claim(ctx context.Context, id, owner, deviceID string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	res, err := s.db.ExecContext(ctx, `UPDATE bss_secrets SET device_id = $3, claimed_at = now()
		WHERE id = $1 AND customer_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid AND device_id IS NULL AND expires_at > now()`,
		id, owner, deviceID)
	if err != nil {
		return fmt.Errorf("claim secret: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrInvalidHandle
	}
	return nil
}

// Open returns the value of a handle claimed for deviceID and deletes it,
// in one statement: a handle opens once.
func (s *Store) Open(ctx context.Context, id, deviceID string) (string, error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}
	var (
		owner  sql.NullString
		sealed []byte
	)
	err := s.db.QueryRowContext(ctx, `DELETE FROM bss_secrets WHERE id = $1 AND device_id = $2 AND expires_at > now()
		RETURNING customer_id::text, sealed`, id, deviceID).Scan(&owner, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrHandleGone
	}
	if err != nil {
		return "", fmt.Errorf("open secret: %w", err)
	}
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("open secret: sealed value too short")
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], additionalData(id, owner.String))
	if err != nil {
		// A changed key, or a row moved or altered in the database.
		return "", errors.New("open secret: the value does not authenticate under the configured key")
	}
	return string(plain), nil
}

// Discard deletes owner's handle, claimed or not. It reports whether one
// was deleted.
func (s *Store) Discard(ctx context.Context, id, owner string) (bool, error) {
	if !s.Enabled() {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM bss_secrets WHERE id = $1 AND customer_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid`, id, owner)
	if err != nil {
		return false, fmt.Errorf("discard secret: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Sweep deletes expired handles and returns how many.
func (s *Store) Sweep(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM bss_secrets WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("sweep secrets: %w", err)
	}
	return res.RowsAffected()
}
