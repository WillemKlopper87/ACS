// Package credentials owns versioned per-device credentials for the one
// rotation flow design doc v3 §11.6 actually specifies as buildable:
// ACS-to-CPE Connection Request. (v3 §11.5 explicitly warns the *other*
// direction — CPE-to-ACS Digest — often can't be rotated remotely at all,
// vendor-dependent; this package doesn't attempt that one.)
//
// The full v3 flow: generate a new credential, queue a SetParameterValues
// pushing it to the CPE, confirm, switch the ACS's own Connection Request
// client to it, keep the old one valid for a grace period, audit the
// revocation. This package models the state machine
// (PENDING -> ACTIVE -> GRACE -> REVOKED); cmd/api's REST handlers drive
// the transitions and cmd/api's connreq_worker reads ActiveForDevice
// instead of a single shared credential when one exists.
package credentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	TypeConnectionRequest = "CONNECTION_REQUEST"
	// TypeCWMPDigest is the CPE->ACS direction: the username/password the
	// device presents on every CWMP session (ManagementServer.Username /
	// .Password). Unlike CONNECTION_REQUEST it activates itself — the
	// first Inform authenticated with a PENDING credential is the proof
	// the CPE applied it (see cmd/acs's Digest lookup hook).
	TypeCWMPDigest = "CWMP_DIGEST"
)

// ValidType reports whether t is a credential_type this package manages.
func ValidType(t string) bool { return t == TypeConnectionRequest || t == TypeCWMPDigest }

const (
	StatusPending = "PENDING"
	StatusActive  = "ACTIVE"
	StatusGrace   = "GRACE"
	StatusRevoked = "REVOKED"
)

var (
	ErrNotFound           = errors.New("credential not found")
	ErrNoActiveCredential = errors.New("no active credential for device")
)

// Credential is a row of the device_credentials table. Password is only
// ever populated by GenerateCredential/repository internals that need it
// to render the SetParameterValues RPC or perform a Connection Request —
// cmd/api's REST layer must never serialize it back over the wire (design
// doc v3 §11.7/§11.8: "secret values must be masked").
type Credential struct {
	ID             string
	DeviceID       string
	CredentialType string
	Version        int
	Username       string
	Password       string
	Status         string
	CommandKey     string
	CreatedAt      time.Time
	ActivatedAt    *time.Time
	RevokedAt      *time.Time
}

// GenerateUsernamePassword produces a fresh random credential pair — 8
// random bytes hex-encoded for the username (readable, still unguessable)
// and 24 random bytes for the password (192 bits, well past any
// brute-force concern for a Digest-style credential).
func GenerateUsernamePassword() (username, password string, err error) {
	u := make([]byte, 8)
	if _, err := rand.Read(u); err != nil {
		return "", "", fmt.Errorf("generate username: %w", err)
	}
	p := make([]byte, 24)
	if _, err := rand.Read(p); err != nil {
		return "", "", fmt.Errorf("generate password: %w", err)
	}
	return "cr-" + hex.EncodeToString(u), hex.EncodeToString(p), nil
}

type Repository struct {
	db  *sql.DB
	gcm cipher.AEAD // nil unless ACS_CREDENTIAL_ENCRYPTION_KEY is configured — passwords stored plaintext otherwise
}

// encPrefix marks a password value as AES-GCM-encrypted (nonce||ciphertext,
// hex-encoded) — distinguishes it from a legacy plaintext row so
// encryption can be turned on for an existing deployment without a data
// migration: old plaintext rows keep working (decrypt is a no-op when the
// prefix is absent), new writes are encrypted going forward.
const encPrefix = "enc:"

// NewRepository builds a credentials repository. encryptionKeySeed, when
// non-empty, is hashed with SHA-256 to derive an AES-256 key — so an
// operator can configure ACS_CREDENTIAL_ENCRYPTION_KEY as any passphrase
// rather than needing to generate an exact-length key by hand — and every
// device_credentials.password is encrypted at rest from then on. Left
// unset (the default), this behaves exactly as it always has: plaintext,
// matching every other "off unless configured, loud warning when it
// isn't" gate in this codebase — the warning itself is logged by
// cmd/api's main(), which has a logger; this package deliberately doesn't.
func NewRepository(db *sql.DB, encryptionKeySeed string) (*Repository, error) {
	if encryptionKeySeed == "" {
		return &Repository{db: db}, nil
	}
	key := sha256.Sum256([]byte(encryptionKeySeed))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("build credential encryption cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build credential encryption GCM mode: %w", err)
	}
	return &Repository{db: db, gcm: gcm}, nil
}

// Encrypted reports whether this repository is encrypting passwords at
// rest — cmd/api's startup log uses this to decide which warning (or
// none) to print.
func (r *Repository) Encrypted() bool {
	return r.gcm != nil
}

// encrypt returns plaintext unchanged when encryption isn't configured
// (the default), otherwise AES-GCM-seals it with a fresh random nonce and
// hex-encodes nonce||ciphertext behind encPrefix.
func (r *Repository) encrypt(plaintext string) (string, error) {
	if r.gcm == nil {
		return plaintext, nil
	}
	nonce := make([]byte, r.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate credential encryption nonce: %w", err)
	}
	sealed := r.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefix + hex.EncodeToString(sealed), nil
}

// decrypt reverses encrypt. A value with no encPrefix is treated as a
// legacy (or encryption-still-disabled) plaintext row and returned as-is
// — this is what lets ACS_CREDENTIAL_ENCRYPTION_KEY be turned on for an
// existing deployment without a backfill migration.
func (r *Repository) decrypt(stored string) (string, error) {
	if r.gcm == nil || !strings.HasPrefix(stored, encPrefix) {
		return stored, nil
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(stored, encPrefix))
	if err != nil {
		return "", fmt.Errorf("decode encrypted credential: %w", err)
	}
	nonceSize := r.gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("encrypted credential too short")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := r.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt credential (wrong key or corrupted row?): %w", err)
	}
	return string(plaintext), nil
}

const credColumns = `id, device_id, credential_type, version, username, password,
	status, command_key, created_at, activated_at, revoked_at`

// Create inserts a new PENDING credential, versioned one past whatever
// the device+type's highest version currently is (starting at 1).
func (r *Repository) Create(ctx context.Context, deviceID, credType, username, password, commandKey string) (*Credential, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create credential tx: %w", err)
	}
	defer tx.Rollback()

	var nextVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1 FROM device_credentials
		WHERE device_id = $1 AND credential_type = $2`, deviceID, credType).Scan(&nextVersion); err != nil {
		return nil, fmt.Errorf("compute next credential version: %w", err)
	}

	encryptedPassword, err := r.encrypt(password)
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	row := tx.QueryRowContext(ctx, `
		INSERT INTO device_credentials (id, device_id, credential_type, version, username, password, command_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+credColumns,
		id, deviceID, credType, nextVersion, username, encryptedPassword, commandKey)

	cred, err := r.scanCredential(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create credential tx: %w", err)
	}
	return cred, nil
}

// ByID fetches one credential.
func (r *Repository) ByID(ctx context.Context, id string) (*Credential, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+credColumns+` FROM device_credentials WHERE id = $1`, id)
	cred, err := r.scanCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return cred, err
}

// ListByDevice returns every credential version for a device, newest
// first — the rotation history an operator reviews before revoking.
func (r *Repository) ListByDevice(ctx context.Context, deviceID string) ([]Credential, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+credColumns+` FROM device_credentials
		WHERE device_id = $1 ORDER BY version DESC`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list device credentials: %w", err)
	}
	defer rows.Close()

	var out []Credential
	for rows.Next() {
		c, err := r.scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ActiveForDevice returns the current ACTIVE credential for a device,
// or ErrNoActiveCredential if the device has never had one rotated in
// (the common case — most devices still use the shared fallback
// credential cmd/api's connreq_worker was already configured with).
func (r *Repository) ActiveForDevice(ctx context.Context, deviceID, credType string) (*Credential, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+credColumns+` FROM device_credentials
		WHERE device_id = $1 AND credential_type = $2 AND status = $3`,
		deviceID, credType, StatusActive)
	cred, err := r.scanCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoActiveCredential
	}
	return cred, err
}

// LookupCWMPDigest finds the live (PENDING, ACTIVE, or GRACE) CWMP_DIGEST
// credential a CPE is presenting by username — the per-Inform lookup
// behind per-device Digest auth. Usernames are random and unique across
// the fleet (GenerateUsernamePassword), so one row at most matches.
func (r *Repository) LookupCWMPDigest(ctx context.Context, username string) (*Credential, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+credColumns+` FROM device_credentials
		WHERE credential_type = $1 AND username = $2 AND status IN ($3, $4, $5)
		ORDER BY version DESC LIMIT 1`,
		TypeCWMPDigest, username, StatusPending, StatusActive, StatusGrace)
	cred, err := r.scanCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return cred, err
}

// Activate promotes a PENDING credential to ACTIVE and demotes whatever
// was previously ACTIVE (if anything) to GRACE, atomically — this is v3
// §11.6 steps 4-5 ("switch the client" + "keep old credential valid for
// grace period") in one transaction, so there's never a moment with zero
// or two ACTIVE credentials for the same device+type.
func (r *Repository) Activate(ctx context.Context, id string) (*Credential, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin activate tx: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `SELECT `+credColumns+` FROM device_credentials WHERE id = $1 FOR UPDATE`, id)
	cred, err := r.scanCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE device_credentials SET status = $2, revoked_at = NULL
		WHERE device_id = $1 AND credential_type = $3 AND status = $4`,
		cred.DeviceID, StatusGrace, cred.CredentialType, StatusActive); err != nil {
		return nil, fmt.Errorf("demote previous active credential: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE device_credentials SET status = $2, activated_at = now() WHERE id = $1`,
		id, StatusActive); err != nil {
		return nil, fmt.Errorf("activate credential: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit activate tx: %w", err)
	}
	return r.ByID(ctx, id)
}

// Revoke ends a credential's validity — normally called on a GRACE-status
// credential once its grace window has passed (v3 §11.6 step 6, "audit
// old credential revocation"), but also valid from PENDING (an abandoned
// rotation the operator decided not to complete).
func (r *Repository) Revoke(ctx context.Context, id string) (*Credential, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE device_credentials SET status = $2, revoked_at = now()
		WHERE id = $1 AND status IN ($3, $4)`,
		id, StatusRevoked, StatusGrace, StatusPending)
	if err != nil {
		return nil, fmt.Errorf("revoke credential: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return r.ByID(ctx, id)
}

type scanner interface {
	Scan(dest ...any) error
}

func (r *Repository) scanCredential(s scanner) (*Credential, error) {
	var c Credential
	var commandKey sql.NullString
	var activatedAt, revokedAt sql.NullTime

	if err := s.Scan(&c.ID, &c.DeviceID, &c.CredentialType, &c.Version, &c.Username, &c.Password,
		&c.Status, &commandKey, &c.CreatedAt, &activatedAt, &revokedAt); err != nil {
		return nil, fmt.Errorf("scan credential: %w", err)
	}
	plaintext, err := r.decrypt(c.Password)
	if err != nil {
		return nil, fmt.Errorf("credential %s: %w", c.ID, err)
	}
	c.Password = plaintext
	if commandKey.Valid {
		c.CommandKey = commandKey.String
	}
	if activatedAt.Valid {
		t := activatedAt.Time
		c.ActivatedAt = &t
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		c.RevokedAt = &t
	}
	return &c, nil
}
