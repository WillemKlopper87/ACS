// Package policy owns compliance-enforcement policy definitions (build
// plan §4 Phase 7 / design doc v3 Phase 7: "Policy engine"). A Policy is
// "devices matching this filter should report this parameter as this
// value" — cmd/acs (enforce.go) checks every Inform's reported
// parameters against active policies and queues a correcting
// SET_PARAMETER the moment a match drifts. This package only owns the
// definitions; cmd/acs owns evaluating and enforcing them, the same
// split scheduler/rollout use for their own workers.
package policy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("policy not found")

// Policy is a row of the policies table.
type Policy struct {
	ID            string
	Name          string
	ModelFilter   *string
	ParameterName string
	DesiredValue  string
	Enabled       bool
	// CustomerID is the policy's tenant owner (audit P0.5) -- nil means
	// platform-global, restricted the same way DeviceGroup.CustomerID is.
	CustomerID *string
	CreatedBy  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const columns = `id, name, model_filter, parameter_name, desired_value, enabled, customer_id, created_by, created_at, updated_at`

func (r *Repository) Create(ctx context.Context, name string, modelFilter *string, parameterName, desiredValue string, customerID *string, createdBy string) (*Policy, error) {
	id := uuid.New().String()
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO policies (id, name, model_filter, parameter_name, desired_value, customer_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+columns,
		id, name, modelFilter, parameterName, desiredValue, customerID, nullIfEmpty(createdBy))
	return scan(row)
}

func (r *Repository) List(ctx context.Context) ([]Policy, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM policies ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	defer rows.Close()

	var out []Policy
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM policies WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete policy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) SetEnabled(ctx context.Context, id string, enabled bool) (*Policy, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE policies SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
	if err != nil {
		return nil, fmt.Errorf("set policy enabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return r.ByID(ctx, id)
}

func (r *Repository) ByID(ctx context.Context, id string) (*Policy, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM policies WHERE id = $1`, id)
	p, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ForDevice returns every enabled policy whose model_filter matches this
// device's manufacturer or product_class (or has no filter at all —
// fleet-wide) AND whose customer_id is either platform-global or equal
// to deviceCustomerID (audit P0.5 critical invariant: a matching model is
// never sufficient authorization to mutate a device across a tenant
// boundary). Called on every Inform (enforce.go), so this stays a simple
// indexed-free scan against what's normally a small table; not meant to
// scale to thousands of policies.
func (r *Repository) ForDevice(ctx context.Context, manufacturer, productClass string, deviceCustomerID *string) ([]Policy, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+columns+` FROM policies
		WHERE enabled AND (model_filter IS NULL OR $1 ILIKE model_filter OR $2 ILIKE model_filter)
			AND (customer_id IS NULL OR customer_id = $3)`,
		manufacturer, productClass, deviceCustomerID)
	if err != nil {
		return nil, fmt.Errorf("list policies for device: %w", err)
	}
	defer rows.Close()

	var out []Policy
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*Policy, error) {
	var p Policy
	var modelFilter, createdBy, customerID sql.NullString
	if err := s.Scan(&p.ID, &p.Name, &modelFilter, &p.ParameterName, &p.DesiredValue, &p.Enabled, &customerID, &createdBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan policy: %w", err)
	}
	if modelFilter.Valid {
		p.ModelFilter = &modelFilter.String
	}
	if customerID.Valid {
		p.CustomerID = &customerID.String
	}
	if createdBy.Valid {
		p.CreatedBy = createdBy.String
	}
	return &p, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
