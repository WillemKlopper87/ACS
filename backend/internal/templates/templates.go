// Package templates owns config templates: a named, reusable set of
// parameter writes (e.g. "Standard WiFi Profile" = SSID + passphrase +
// channel) that can be bulk-applied to an arbitrary device selection or a
// device_groups group on demand, and optionally auto-applied the moment a
// new device's first BOOTSTRAP Inform arrives — the same model_filter
// concept internal/policy already uses for continuous compliance,
// applied here to one-shot initial provisioning instead of ongoing drift
// correction.
//
// A template's parameter paths are plain TR-181 (Device:2) paths, the
// same convention every other write in this codebase already uses
// uniformly across the fleet — a template written once genuinely applies
// across different manufacturers/models, as long as they share that data
// model root. Older TR-098/IGD:1 devices (a different root entirely)
// aren't covered — see build plan's "data_model_root branching" open
// item; this package doesn't pretend to solve that.
package templates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("config template not found")

// ParameterWrite is one parameter this template sets. Duplicated rather
// than importing internal/jobs.ParameterWrite, matching this codebase's
// established convention of not sharing wire/payload types across
// packages that only meet at a REST/job boundary.
type ParameterWrite struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

// Template is a row of the config_templates table.
type Template struct {
	ID          string
	Name        string
	Description string
	Parameters  []ParameterWrite
	ModelFilter *string
	AutoApply   bool
	// CustomerID is the template's tenant owner (audit P0.4) -- nil means
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

const columns = `id, name, description, parameters, model_filter, auto_apply, customer_id, created_by, created_at, updated_at`

func (r *Repository) Create(ctx context.Context, name, description string, params []ParameterWrite, modelFilter *string, autoApply bool, customerID *string, createdBy string) (*Template, error) {
	if len(params) == 0 {
		return nil, fmt.Errorf("template must have at least one parameter")
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal template parameters: %w", err)
	}

	id := uuid.New().String()
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO config_templates (id, name, description, parameters, model_filter, auto_apply, customer_id, created_by)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)
		RETURNING `+columns,
		id, name, description, paramsJSON, modelFilter, autoApply, customerID, nullIfEmpty(createdBy))
	return scan(row)
}

func (r *Repository) List(ctx context.Context) ([]Template, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM config_templates ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list config templates: %w", err)
	}
	defer rows.Close()

	var out []Template
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (r *Repository) ByID(ctx context.Context, id string) (*Template, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM config_templates WHERE id = $1`, id)
	t, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM config_templates WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete config template: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MatchingAutoApply returns every auto_apply template whose model_filter
// matches this manufacturer/product_class — same ILIKE-against-either-
// field matching internal/policy.Repository.ForDevice already uses, for
// consistency across this codebase's two model_filter consumers.
//
// deviceCustomerID additionally restricts matches to templates owned by
// that same customer, plus platform-global ones (audit P0.4 critical
// invariant: a matching manufacturer/model is never sufficient
// authorization to push a tenant-created template onto another tenant's
// device). Pass nil for an unassigned device, which then only matches
// platform-global templates.
func (r *Repository) MatchingAutoApply(ctx context.Context, manufacturer, productClass string, deviceCustomerID *string) ([]Template, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+columns+` FROM config_templates
		WHERE auto_apply AND model_filter IS NOT NULL AND ($1 ILIKE model_filter OR $2 ILIKE model_filter)
			AND (customer_id IS NULL OR customer_id = $3)`,
		manufacturer, productClass, deviceCustomerID)
	if err != nil {
		return nil, fmt.Errorf("list auto-apply templates: %w", err)
	}
	defer rows.Close()

	var out []Template
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*Template, error) {
	var t Template
	var description, modelFilter, createdBy sql.NullString
	var customerID sql.NullString
	var paramsRaw []byte

	if err := s.Scan(&t.ID, &t.Name, &description, &paramsRaw, &modelFilter, &t.AutoApply, &customerID, &createdBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan config template: %w", err)
	}
	if description.Valid {
		t.Description = description.String
	}
	if modelFilter.Valid {
		s := modelFilter.String
		t.ModelFilter = &s
	}
	if customerID.Valid {
		c := customerID.String
		t.CustomerID = &c
	}
	if createdBy.Valid {
		t.CreatedBy = createdBy.String
	}
	if len(paramsRaw) > 0 {
		if err := json.Unmarshal(paramsRaw, &t.Parameters); err != nil {
			return nil, fmt.Errorf("unmarshal template parameters: %w", err)
		}
	}
	return &t, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
