package vpn

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
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	StatusEnrolled = "ENROLLED"
	StatusRevoked  = "REVOKED"
)

var (
	ErrNotFound        = errors.New("vpn peer not found")
	ErrAlreadyEnrolled = errors.New("device already has a vpn peer")
)

// Peer is a row of device_vpn_peers. PrivateKey is only ever populated by
// EnrollDevice's return value and GetPeerConfig — cmd/api's REST layer
// must never include it in a list response, same rule as every other
// secret type in this codebase (internal/credentials, internal/cliaccess).
type Peer struct {
	ID         string     `json:"id"`
	DeviceID   string     `json:"device_id"`
	PublicKey  string     `json:"public_key"`
	PrivateKey string     `json:"private_key,omitempty"`
	OverlayIP  string     `json:"overlay_ip"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type Repository struct {
	db     *sql.DB
	gcm    cipher.AEAD // nil unless ACS_CREDENTIAL_ENCRYPTION_KEY is configured, same convention as internal/credentials and internal/cliaccess
	subnet *net.IPNet
}

const encPrefix = "enc:"

// NewRepository mirrors internal/cliaccess.NewRepository's encryption
// setup exactly (deliberately duplicated, not shared — see that
// package's doc comment for why). subnet is the overlay network new
// peers are allocated from (design doc's recommended 10.99.0.0/16).
func NewRepository(db *sql.DB, encryptionKeySeed string, subnet *net.IPNet) (*Repository, error) {
	r := &Repository{db: db, subnet: subnet}
	if encryptionKeySeed == "" {
		return r, nil
	}
	key := sha256.Sum256([]byte(encryptionKeySeed))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("build vpn peer encryption cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build vpn peer encryption GCM mode: %w", err)
	}
	r.gcm = gcm
	return r, nil
}

func (r *Repository) encrypt(plaintext string) (string, error) {
	if r.gcm == nil {
		return plaintext, nil
	}
	nonce := make([]byte, r.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate vpn peer encryption nonce: %w", err)
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
		return "", fmt.Errorf("decode encrypted vpn peer key: %w", err)
	}
	nonceSize := r.gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("encrypted vpn peer key too short")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := r.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt vpn peer key (wrong key or corrupted row?): %w", err)
	}
	return string(plaintext), nil
}

const peerColumns = `id, device_id, public_key, private_key, overlay_ip, status, created_at, revoked_at`

// EnrollDevice generates a fresh keypair, allocates the next free overlay
// IP, and inserts the peer row — all in one transaction so a concurrent
// enrollment can't allocate the same overlay IP twice.
func (r *Repository) EnrollDevice(ctx context.Context, deviceID string) (*Peer, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin enroll transaction: %w", err)
	}
	defer tx.Rollback()

	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM device_vpn_peers WHERE device_id = $1 AND status = $2`, deviceID, StatusEnrolled).Scan(&existing); err != nil {
		return nil, fmt.Errorf("check existing peer: %w", err)
	}
	if existing > 0 {
		return nil, ErrAlreadyEnrolled
	}

	used := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT overlay_ip FROM device_vpn_peers WHERE status = $1`, StatusEnrolled)
	if err != nil {
		return nil, fmt.Errorf("list allocated overlay ips: %w", err)
	}
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan overlay ip: %w", err)
		}
		used[ip] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	overlayIP, err := AllocateOverlayIP(r.subnet, used)
	if err != nil {
		return nil, err
	}

	keys, err := GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	encryptedPrivate, err := r.encrypt(keys.PrivateKey)
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	row := tx.QueryRowContext(ctx, `
		INSERT INTO device_vpn_peers (id, device_id, public_key, private_key, overlay_ip, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+peerColumns,
		id, deviceID, keys.PublicKey, encryptedPrivate, overlayIP, StatusEnrolled)
	peer, err := r.scanPeer(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit enroll transaction: %w", err)
	}
	peer.PrivateKey = keys.PrivateKey // return the plaintext form, not what got persisted
	return peer, nil
}

// ListPeers returns every peer, private keys always redacted — this is
// the admin panel's list view, never a place a raw key should appear.
// ListPeers returns every peer, unfiltered. Callers enforce tenancy
// scope (audit P2.1/M-12) — see ListPeersForCustomers for the scoped
// equivalent, the same split devices.List/deviceScope already uses.
func (r *Repository) ListPeers(ctx context.Context) ([]Peer, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+peerColumns+` FROM device_vpn_peers ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list vpn peers: %w", err)
	}
	defer rows.Close()

	var out []Peer
	for rows.Next() {
		p, err := r.scanPeer(rows)
		if err != nil {
			return nil, err
		}
		p.PrivateKey = ""
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ListPeersForCustomers returns every peer whose device belongs to one
// of customerIDs (audit P2.1/M-12) — what a scoped operator's peer list
// must be restricted to, since a peer row names its device and overlay
// IP, both cross-tenant identifying details.
func (r *Repository) ListPeersForCustomers(ctx context.Context, customerIDs []string) ([]Peer, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.id, p.device_id, p.public_key, p.private_key, p.overlay_ip, p.status, p.created_at, p.revoked_at
		FROM device_vpn_peers p
		JOIN devices d ON d.id = p.device_id
		WHERE d.customer_id = ANY($1)
		ORDER BY p.created_at DESC`, customerIDs)
	if err != nil {
		return nil, fmt.Errorf("list vpn peers for customers: %w", err)
	}
	defer rows.Close()

	var out []Peer
	for rows.Next() {
		p, err := r.scanPeer(rows)
		if err != nil {
			return nil, err
		}
		p.PrivateKey = ""
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPeerConfig returns the device's peer row with its private key
// decrypted — used only to render a downloadable client config, never
// exposed through the list endpoint.
func (r *Repository) GetPeerConfig(ctx context.Context, deviceID string) (*Peer, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+peerColumns+` FROM device_vpn_peers WHERE device_id = $1 AND status = $2`, deviceID, StatusEnrolled)
	peer, err := r.scanPeer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	decrypted, err := r.decrypt(peer.PrivateKey)
	if err != nil {
		return nil, err
	}
	peer.PrivateKey = decrypted
	return peer, nil
}

// RevokePeer marks a peer REVOKED and frees its overlay IP for reuse —
// it does not (cannot, from this process) tear down a live tunnel, since
// no OS-level WireGuard interface is managed here; see this package's
// doc comment.
func (r *Repository) RevokePeer(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE device_vpn_peers SET status = $1, revoked_at = now() WHERE id = $2 AND status = $3`, StatusRevoked, id, StatusEnrolled)
	if err != nil {
		return fmt.Errorf("revoke vpn peer: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func (r *Repository) scanPeer(s scanner) (*Peer, error) {
	var p Peer
	var revokedAt sql.NullTime
	if err := s.Scan(&p.ID, &p.DeviceID, &p.PublicKey, &p.PrivateKey, &p.OverlayIP, &p.Status, &p.CreatedAt, &revokedAt); err != nil {
		return nil, fmt.Errorf("scan vpn peer: %w", err)
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		p.RevokedAt = &t
	}
	return &p, nil
}
