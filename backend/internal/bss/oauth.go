// OAuth2 client-credentials auth (RFC 6749 §4.4) for cmd/bssadapter —
// the production-grade replacement for the single-shared-token interim
// mechanism the integration guide always flagged as temporary. Each
// registered BSS/CRM integration gets its own client_id/client_secret
// pair, exchanged for a short-lived bearer JWT at POST /bss/v1/oauth/token
// (cmd/bssadapter/oauth_handlers.go) rather than presenting a
// long-lived static credential on every request.
package bss

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidClientCredentials = errors.New("invalid client credentials")

// OAuthClient is a row of bss_oauth_clients. ClientSecretHash is never
// exposed outside this package — ListClients/CreateClient's returned
// struct omits it entirely (see scanOAuthClient).
type OAuthClient struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	ClientID  string     `json:"client_id"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type OAuthRepository struct {
	db *sql.DB
}

func NewOAuthRepository(db *sql.DB) *OAuthRepository {
	return &OAuthRepository{db: db}
}

// CreateClient generates a fresh client_id/client_secret pair — 16
// random bytes hex-encoded for the ID (readable in a UI, still
// unguessable), 32 random bytes for the secret (256 bits) — and stores
// only the secret's bcrypt hash. The plaintext secret is returned once,
// here, and never again; same "shown once" rule as every other generated
// credential in this codebase.
func (r *OAuthRepository) CreateClient(ctx context.Context, name string) (client *OAuthClient, plaintextSecret string, err error) {
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, "", fmt.Errorf("generate client_id: %w", err)
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, "", fmt.Errorf("generate client_secret: %w", err)
	}
	clientID := "bss-" + hex.EncodeToString(idBytes)
	clientSecret := hex.EncodeToString(secretBytes)

	hash, err := bcrypt.GenerateFromPassword([]byte(clientSecret), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", fmt.Errorf("hash client_secret: %w", err)
	}

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO bss_oauth_clients (id, name, client_id, client_secret_hash)
		VALUES ($1, $2, $3, $4)
		RETURNING id, name, client_id, created_at, revoked_at`,
		uuid.New().String(), name, clientID, string(hash))
	c, err := scanOAuthClient(row)
	if err != nil {
		return nil, "", err
	}
	return c, clientSecret, nil
}

func (r *OAuthRepository) ListClients(ctx context.Context) ([]OAuthClient, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, client_id, created_at, revoked_at FROM bss_oauth_clients ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list oauth clients: %w", err)
	}
	defer rows.Close()

	var out []OAuthClient
	for rows.Next() {
		c, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *OAuthRepository) RevokeClient(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE bss_oauth_clients SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("revoke oauth client: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("oauth client not found or already revoked")
	}
	return nil
}

// VerifyCredentials checks a client_id/client_secret pair against the
// stored bcrypt hash — constant-time by construction (bcrypt.CompareHashAndPassword
// is designed to be), and rejects a revoked client even with a correct secret.
func (r *OAuthRepository) VerifyCredentials(ctx context.Context, clientID, clientSecret string) error {
	var hash string
	var revokedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT client_secret_hash, revoked_at FROM bss_oauth_clients WHERE client_id = $1`, clientID).Scan(&hash, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidClientCredentials
	}
	if err != nil {
		return fmt.Errorf("look up oauth client: %w", err)
	}
	if revokedAt.Valid {
		return ErrInvalidClientCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(clientSecret)); err != nil {
		return ErrInvalidClientCredentials
	}
	return nil
}

type oauthScanner interface {
	Scan(dest ...any) error
}

func scanOAuthClient(s oauthScanner) (*OAuthClient, error) {
	var c OAuthClient
	var revokedAt sql.NullTime
	if err := s.Scan(&c.ID, &c.Name, &c.ClientID, &c.CreatedAt, &revokedAt); err != nil {
		return nil, fmt.Errorf("scan oauth client: %w", err)
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		c.RevokedAt = &t
	}
	return &c, nil
}
