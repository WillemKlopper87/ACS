package operators

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrUsernameTaken = errors.New("username already exists")

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const operatorColumns = `id, username, COALESCE(email, ''), password_hash, role, created_at, updated_at, token_version, global_access`

// Create inserts a new operator with an already-hashed password — callers
// are responsible for bcrypt-hashing, this package never sees a plaintext
// password on the write path (only Login's caller does, briefly, to
// compare it). email is optional — empty string means self-service
// password reset isn't available for this operator (only the superadmin
// reset-from-account-page path is).
func (r *Repository) Create(ctx context.Context, username, email, passwordHash, role string) (*Operator, error) {
	id := uuid.New().String()
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO operators (id, username, email, password_hash, role)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+operatorColumns,
		id, username, nullIfEmpty(email), passwordHash, role)

	op, err := scanOperator(row)
	if isUniqueViolation(err) {
		return nil, ErrUsernameTaken
	}
	return op, err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ByUsername fetches an operator for login. Returns sql.ErrNoRows
// (wrapped) if no such username exists.
func (r *Repository) ByUsername(ctx context.Context, username string) (*Operator, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+operatorColumns+` FROM operators WHERE username = $1`, username)
	return scanOperator(row)
}

// Count is used at startup to decide whether the bootstrap-admin env vars
// should create the very first operator (chicken-and-egg: there's no
// admin yet to create one through the API).
func (r *Repository) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM operators`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count operators: %w", err)
	}
	return n, nil
}

// List returns every operator (admin-facing operator management), never
// including password hashes in what callers expose over the wire — that's
// the REST handler's job to trim, this just returns the row.
func (r *Repository) List(ctx context.Context) ([]Operator, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+operatorColumns+` FROM operators ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list operators: %w", err)
	}
	defer rows.Close()

	var out []Operator
	for rows.Next() {
		op, err := scanOperator(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *op)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOperator(s scanner) (*Operator, error) {
	var op Operator
	if err := s.Scan(&op.ID, &op.Username, &op.Email, &op.PasswordHash, &op.Role, &op.CreatedAt, &op.UpdatedAt, &op.TokenVersion, &op.GlobalAccess); err != nil {
		return nil, fmt.Errorf("scan operator: %w", err)
	}
	return &op, nil
}

// SetGlobalAccess grants or revokes the OPERATOR_GLOBAL entitlement (audit
// P0.1) — must be called explicitly by a superadmin; nothing infers this
// from the absence of operator_scopes rows.
func (r *Repository) SetGlobalAccess(ctx context.Context, operatorID string, global bool) error {
	res, err := r.db.ExecContext(ctx, `UPDATE operators SET global_access = $1, updated_at = now() WHERE id = $2`, global, operatorID)
	if err != nil {
		return fmt.Errorf("set operator global access: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set operator global access: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ByID fetches an operator by id — used by admin-facing endpoints that
// address an operator directly (scope/global-access assignment) rather
// than by the JWT subject.
func (r *Repository) ByID(ctx context.Context, id string) (*Operator, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+operatorColumns+` FROM operators WHERE id = $1`, id)
	return scanOperator(row)
}

// isUniqueViolation checks for Postgres error code 23505 (unique_violation)
// — this package's own repositories are the only ones that need it so far;
// every earlier repository's unique constraint (e.g. devices.oui_serial)
// is upserted via ON CONFLICT rather than relying on the write failing.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
