package operators

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// PermissionRepository owns role_permissions (migration 0032) — split from
// Repository (operator identity/auth) since the two have different callers
// (login/operator-CRUD vs. the superadmin permission-matrix screen) and
// different bootstrap needs (this one seeds defaults on first use).
type PermissionRepository struct {
	db *sql.DB
}

func NewPermissionRepository(db *sql.DB) *PermissionRepository {
	return &PermissionRepository{db: db}
}

// All returns the full role x permission matrix, seeding
// defaultPermissions for any (role, permission) pair that has no row yet
// — so a freshly migrated deployment (or a newly added permission key in
// a future release) always returns a complete matrix rather than gaps the
// UI would have to guess about.
func (r *PermissionRepository) All(ctx context.Context) (map[string]map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT role, permission, granted FROM role_permissions`)
	if err != nil {
		return nil, fmt.Errorf("list role permissions: %w", err)
	}
	defer rows.Close()

	out := map[string]map[string]bool{}
	for _, role := range []string{RoleManager, RoleNOC, RoleReadOnly} {
		out[role] = map[string]bool{}
	}
	for rows.Next() {
		var role, perm string
		var granted bool
		if err := rows.Scan(&role, &perm, &granted); err != nil {
			return nil, fmt.Errorf("scan role permission: %w", err)
		}
		if out[role] == nil {
			out[role] = map[string]bool{}
		}
		out[role][perm] = granted
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for role, defaults := range defaultPermissions {
		for perm, granted := range defaults {
			if _, ok := out[role][perm]; !ok {
				out[role][perm] = granted
			}
		}
	}
	return out, nil
}

// Set writes one (role, permission) -> granted row, upserting.
func (r *PermissionRepository) Set(ctx context.Context, role, permission string, granted bool) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO role_permissions (role, permission, granted)
		VALUES ($1, $2, $3)
		ON CONFLICT (role, permission) DO UPDATE SET granted = EXCLUDED.granted
	`, role, permission, granted)
	if err != nil {
		return fmt.Errorf("set role permission: %w", err)
	}
	return nil
}

// Has reports whether role currently has permission — superadmin always
// does (never consults the table, see migration 0032's comment); every
// other role falls back to defaultPermissions if no row has been written
// yet (fresh deployment behaves exactly like the old 3-tier rank system
// until a superadmin actually changes something).
func (r *PermissionRepository) Has(ctx context.Context, role, permission string) (bool, error) {
	if role == RoleSuperAdmin {
		return true, nil
	}
	var granted bool
	err := r.db.QueryRowContext(ctx, `SELECT granted FROM role_permissions WHERE role = $1 AND permission = $2`, role, permission).Scan(&granted)
	if errors.Is(err, sql.ErrNoRows) {
		if defaults, ok := defaultPermissions[role]; ok {
			return defaults[permission], nil
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check role permission: %w", err)
	}
	return granted, nil
}

// --- password reset tokens (migration 0032) ---

var ErrTokenInvalid = errors.New("reset token invalid, expired, or already used")

const resetTokenTTL = 4 * time.Hour

// CreateResetToken generates a fresh 32-byte random token, valid for 4
// hours (per the user's spec), and records it against operatorID.
func (r *Repository) CreateResetToken(ctx context.Context, operatorID string) (string, time.Time, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, fmt.Errorf("generate reset token: %w", err)
	}
	token := hex.EncodeToString(b)
	expiresAt := time.Now().UTC().Add(resetTokenTTL)

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO password_reset_tokens (token, operator_id, expires_at) VALUES ($1, $2, $3)
	`, token, operatorID, expiresAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("insert reset token: %w", err)
	}
	return token, expiresAt, nil
}

// ConsumeResetToken atomically validates a token (exists, unexpired,
// unused) and marks it used, returning the operator it belonged to — a
// token can only ever complete one password reset.
func (r *Repository) ConsumeResetToken(ctx context.Context, token string) (*Operator, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin consume reset token tx: %w", err)
	}
	defer tx.Rollback()

	var operatorID string
	var expiresAt time.Time
	var usedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT operator_id, expires_at, used_at FROM password_reset_tokens WHERE token = $1 FOR UPDATE
	`, token).Scan(&operatorID, &expiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("load reset token: %w", err)
	}
	if usedAt.Valid || time.Now().UTC().After(expiresAt) {
		return nil, ErrTokenInvalid
	}

	if _, err := tx.ExecContext(ctx, `UPDATE password_reset_tokens SET used_at = now() WHERE token = $1`, token); err != nil {
		return nil, fmt.Errorf("mark reset token used: %w", err)
	}

	row := tx.QueryRowContext(ctx, `SELECT `+operatorColumns+` FROM operators WHERE id = $1`, operatorID)
	op, err := scanOperator(row)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit consume reset token tx: %w", err)
	}
	return op, nil
}

// UpdatePassword overwrites an operator's password hash — used by both
// the superadmin "reset this user's password" action and the self-service
// token-based reset flow.
func (r *Repository) UpdatePassword(ctx context.Context, operatorID, passwordHash string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE operators SET password_hash = $2, updated_at = now() WHERE id = $1`, operatorID, passwordHash)
	if err != nil {
		return fmt.Errorf("update operator password: %w", err)
	}
	return nil
}

// ByEmail fetches an operator by email — the self-service reset request
// entry point. Returns sql.ErrNoRows (wrapped) if no operator has that
// email, which the REST handler deliberately does not distinguish from
// success in its response (no user-enumeration via the reset endpoint).
func (r *Repository) ByEmail(ctx context.Context, email string) (*Operator, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+operatorColumns+` FROM operators WHERE email = $1`, email)
	return scanOperator(row)
}
