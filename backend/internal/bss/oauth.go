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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidClientCredentials = errors.New("invalid client credentials")
var ErrInvalidOAuthPolicy = errors.New("invalid oauth client policy")

const (
	ScopeTMFRead        = "tmf:read"
	ScopeTMFWrite       = "tmf:write"
	ScopeTMFExecute     = "tmf:execute"
	ScopeTMFAcknowledge = "tmf:acknowledge"
)

var validTMFScopes = map[string]struct{}{
	ScopeTMFRead:        {},
	ScopeTMFWrite:       {},
	ScopeTMFExecute:     {},
	ScopeTMFAcknowledge: {},
}

// AllTMFScopes is the full northbound permission set. It is returned as a
// new slice so callers cannot mutate package state.
func AllTMFScopes() []string {
	return []string{ScopeTMFRead, ScopeTMFWrite, ScopeTMFExecute, ScopeTMFAcknowledge}
}

// OAuthPolicy is the authorization boundary attached to one integration.
// A client with scopes must be either explicitly fleet-wide or restricted
// to at least one account. An empty policy is allowed so an integration can
// be registered before TMF access is granted; it authenticates but cannot
// call /tmf-api routes.
type OAuthPolicy struct {
	Scopes       []string `json:"scopes"`
	AccountIDs   []string `json:"account_ids"`
	GlobalAccess bool     `json:"global_access"`
}

func NormalizeOAuthPolicy(policy OAuthPolicy) (OAuthPolicy, error) {
	scopes := uniqueTrimmed(policy.Scopes)
	accounts := uniqueTrimmed(policy.AccountIDs)
	for _, scope := range scopes {
		if _, ok := validTMFScopes[scope]; !ok {
			return OAuthPolicy{}, fmt.Errorf("%w: unsupported scope %q", ErrInvalidOAuthPolicy, scope)
		}
	}
	if policy.GlobalAccess && len(accounts) > 0 {
		return OAuthPolicy{}, fmt.Errorf("%w: global_access cannot be combined with account_ids", ErrInvalidOAuthPolicy)
	}
	if len(scopes) > 0 && !policy.GlobalAccess && len(accounts) == 0 {
		return OAuthPolicy{}, fmt.Errorf("%w: scoped clients require global_access or at least one account_id", ErrInvalidOAuthPolicy)
	}
	return OAuthPolicy{Scopes: scopes, AccountIDs: accounts, GlobalAccess: policy.GlobalAccess}, nil
}

func uniqueTrimmed(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// OAuthClient is a row of bss_oauth_clients. ClientSecretHash is never
// exposed outside this package — ListClients/CreateClient's returned
// struct omits it entirely (see scanOAuthClient).
type OAuthClient struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	ClientID     string     `json:"client_id"`
	Scopes       []string   `json:"scopes"`
	AccountIDs   []string   `json:"account_ids"`
	GlobalAccess bool       `json:"global_access"`
	CreatedAt    time.Time  `json:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

type OAuthRepository struct {
	db *sql.DB
}

func NewOAuthRepository(db *sql.DB) *OAuthRepository {
	return &OAuthRepository{db: db}
}

// CreateClient preserves the historical helper for internal callers, but
// fails closed for TMF authorization: the client receives no TMF scopes.
// New admin flows should call CreateClientWithPolicy explicitly.
func (r *OAuthRepository) CreateClient(ctx context.Context, name string) (*OAuthClient, string, error) {
	return r.CreateClientWithPolicy(ctx, name, OAuthPolicy{})
}

// CreateClientWithPolicy generates a fresh client_id/client_secret pair
// and persists an explicit TMF authorization policy. Only the bcrypt hash
// of the secret is stored; plaintext is returned once.
func (r *OAuthRepository) CreateClientWithPolicy(ctx context.Context, name string, policy OAuthPolicy) (client *OAuthClient, plaintextSecret string, err error) {
	policy, err = NormalizeOAuthPolicy(policy)
	if err != nil {
		return nil, "", err
	}
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
	scopesJSON, _ := json.Marshal(policy.Scopes)
	accountsJSON, _ := json.Marshal(policy.AccountIDs)

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO bss_oauth_clients (id, name, client_id, client_secret_hash, scopes, account_ids, global_access)
		VALUES ($1, $2, $3, $4,
			ARRAY(SELECT jsonb_array_elements_text($5::jsonb)),
			ARRAY(SELECT jsonb_array_elements_text($6::jsonb)), $7)
		RETURNING id, name, client_id, created_at, revoked_at,
			array_to_json(scopes), array_to_json(account_ids), global_access`,
		uuid.New().String(), name, clientID, string(hash), string(scopesJSON), string(accountsJSON), policy.GlobalAccess)
	c, err := scanOAuthClient(row)
	if err != nil {
		return nil, "", err
	}
	return c, clientSecret, nil
}

func (r *OAuthRepository) ListClients(ctx context.Context) ([]OAuthClient, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, client_id, created_at, revoked_at,
			array_to_json(scopes), array_to_json(account_ids), global_access
		FROM bss_oauth_clients ORDER BY created_at DESC`)
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

// IsRevoked reports whether clientID (embedded in an already-issued
// access token's claims) has since been revoked. Unknown clients fail closed.
func (r *OAuthRepository) IsRevoked(ctx context.Context, clientID string) (bool, error) {
	var revokedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT revoked_at FROM bss_oauth_clients WHERE client_id = $1`, clientID).Scan(&revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return true, fmt.Errorf("check oauth client revocation: %w", err)
	}
	return revokedAt.Valid, nil
}

// AuthenticateClient checks the client secret and returns the current
// authorization policy used to mint the short-lived token.
func (r *OAuthRepository) AuthenticateClient(ctx context.Context, clientID, clientSecret string) (*OAuthClient, error) {
	var hash string
	var revokedAt sql.NullTime
	var id, name string
	var createdAt time.Time
	var scopesRaw, accountsRaw []byte
	var globalAccess bool
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, client_secret_hash, created_at, revoked_at,
			array_to_json(scopes), array_to_json(account_ids), global_access
		FROM bss_oauth_clients WHERE client_id = $1`, clientID).
		Scan(&id, &name, &hash, &createdAt, &revokedAt, &scopesRaw, &accountsRaw, &globalAccess)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidClientCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("look up oauth client: %w", err)
	}
	if revokedAt.Valid || bcrypt.CompareHashAndPassword([]byte(hash), []byte(clientSecret)) != nil {
		return nil, ErrInvalidClientCredentials
	}
	client := &OAuthClient{ID: id, Name: name, ClientID: clientID, CreatedAt: createdAt, GlobalAccess: globalAccess}
	if revokedAt.Valid {
		t := revokedAt.Time
		client.RevokedAt = &t
	}
	if err := json.Unmarshal(scopesRaw, &client.Scopes); err != nil {
		return nil, fmt.Errorf("decode oauth scopes: %w", err)
	}
	if err := json.Unmarshal(accountsRaw, &client.AccountIDs); err != nil {
		return nil, fmt.Errorf("decode oauth accounts: %w", err)
	}
	return client, nil
}

// VerifyCredentials remains for callers that only need authentication.
func (r *OAuthRepository) VerifyCredentials(ctx context.Context, clientID, clientSecret string) error {
	_, err := r.AuthenticateClient(ctx, clientID, clientSecret)
	return err
}

type oauthScanner interface {
	Scan(dest ...any) error
}

func scanOAuthClient(s oauthScanner) (*OAuthClient, error) {
	var c OAuthClient
	var revokedAt sql.NullTime
	var scopesRaw, accountsRaw []byte
	if err := s.Scan(&c.ID, &c.Name, &c.ClientID, &c.CreatedAt, &revokedAt, &scopesRaw, &accountsRaw, &c.GlobalAccess); err != nil {
		return nil, fmt.Errorf("scan oauth client: %w", err)
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		c.RevokedAt = &t
	}
	if err := json.Unmarshal(scopesRaw, &c.Scopes); err != nil {
		return nil, fmt.Errorf("decode oauth scopes: %w", err)
	}
	if err := json.Unmarshal(accountsRaw, &c.AccountIDs); err != nil {
		return nil, fmt.Errorf("decode oauth accounts: %w", err)
	}
	return &c, nil
}
