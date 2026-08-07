// Package cliaccess owns per-device SSH/Telnet credentials for the device
// console's "remote shell" panel (admin-platform backlog). Scaffolded per
// the user's explicit "build now, functional later" call — these
// credentials are for the device's own OS-level shell account, a
// completely different thing from internal/credentials' CWMP Connection
// Request credential (which the ACS itself generates and rotates). The
// ACS reaching a device's SSH/Telnet port has the identical CGNAT
// reachability constraint as Connection Request, just on port 22/23
// instead of 7547 — see internal/stun's doc comment for the Annex G side
// of that same constraint.
package cliaccess

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
	ProtocolSSH    = "SSH"
	ProtocolTelnet = "TELNET"
)

var ErrNotFound = errors.New("cli credential not found")

// Credential is a row of device_cli_credentials. Password is only ever
// populated by repository internals that need it to dial the device —
// cmd/api's REST layer must never serialize it back over the wire, same
// rule as internal/credentials.Credential.
type Credential struct {
	ID        string
	DeviceID  string
	Protocol  string
	Host      string
	Port      int
	Username  string
	Password  string
	CreatedAt time.Time
}

type Repository struct {
	db  *sql.DB
	gcm cipher.AEAD // nil unless ACS_CREDENTIAL_ENCRYPTION_KEY is configured, same convention as internal/credentials
}

const encPrefix = "enc:"

// NewRepository mirrors internal/credentials.NewRepository exactly — same
// key-derivation, same encPrefix scheme, same "off unless configured"
// default — deliberately duplicated rather than shared, since the two
// packages' credential types have nothing else in common and this project's
// existing convention is duplication over cross-package coupling for
// exactly this shape of thing (see internal/jobs's payload structs).
func NewRepository(db *sql.DB, encryptionKeySeed string) (*Repository, error) {
	if encryptionKeySeed == "" {
		return &Repository{db: db}, nil
	}
	key := sha256.Sum256([]byte(encryptionKeySeed))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("build cli credential encryption cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build cli credential encryption GCM mode: %w", err)
	}
	return &Repository{db: db, gcm: gcm}, nil
}

func (r *Repository) encrypt(plaintext string) (string, error) {
	if r.gcm == nil {
		return plaintext, nil
	}
	nonce := make([]byte, r.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate cli credential encryption nonce: %w", err)
	}
	sealed := r.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefix + hex.EncodeToString(sealed), nil
}

func (r *Repository) decrypt(stored string) (string, error) {
	if r.gcm == nil || !strings.HasPrefix(stored, encPrefix) {
		return stored, nil
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(stored, encPrefix))
	if err != nil {
		return "", fmt.Errorf("decode encrypted cli credential: %w", err)
	}
	nonceSize := r.gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("encrypted cli credential too short")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := r.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt cli credential (wrong key or corrupted row?): %w", err)
	}
	return string(plaintext), nil
}

const credColumns = `id, device_id, protocol, host, port, username, password, created_at`

func (r *Repository) Create(ctx context.Context, deviceID, protocol, host string, port int, username, password string) (*Credential, error) {
	encryptedPassword, err := r.encrypt(password)
	if err != nil {
		return nil, err
	}
	id := uuid.New().String()
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO device_cli_credentials (id, device_id, protocol, host, port, username, password)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+credColumns,
		id, deviceID, protocol, host, port, username, encryptedPassword)
	return r.scanCredential(row)
}

func (r *Repository) ByID(ctx context.Context, id string) (*Credential, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+credColumns+` FROM device_cli_credentials WHERE id = $1`, id)
	cred, err := r.scanCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return cred, err
}

func (r *Repository) ListByDevice(ctx context.Context, deviceID string) ([]Credential, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+credColumns+` FROM device_cli_credentials WHERE device_id = $1 ORDER BY created_at DESC`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list device cli credentials: %w", err)
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

func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM device_cli_credentials WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete cli credential: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func (r *Repository) scanCredential(s scanner) (*Credential, error) {
	var c Credential
	if err := s.Scan(&c.ID, &c.DeviceID, &c.Protocol, &c.Host, &c.Port, &c.Username, &c.Password, &c.CreatedAt); err != nil {
		return nil, fmt.Errorf("scan cli credential: %w", err)
	}
	plaintext, err := r.decrypt(c.Password)
	if err != nil {
		return nil, fmt.Errorf("cli credential %s: %w", c.ID, err)
	}
	c.Password = plaintext
	return &c, nil
}
