// Package bss implements the account-device mapping and order-dispatch
// logic behind the BSS-facing adapter (build plan §5, Phase 8). It never
// talks CWMP and never writes to the jobs table directly — job creation
// happens through the same internal ACS REST API any operator uses
// (internal/bss/acsclient.go), keeping the process boundary in build plan
// §5.1 real rather than just conceptual.
package bss

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const (
	StatusPendingActive = "PENDING_ACTIVE"
	StatusActive        = "ACTIVE"
	StatusSuspended     = "SUSPENDED"
	StatusTerminated    = "TERMINATED"
)

// ErrDeviceNotFound is returned when a mapping request's oui_serial
// doesn't match any known device. The reference internal_bss_adapter.go
// draft's RegisterDeviceMapping accepted any device_uuid/oui_serial
// without checking — this is the validation it was missing, and the
// reason mapping creation needs a devices table lookup rather than just
// writing whatever the caller sent.
var ErrDeviceNotFound = errors.New("no device found for oui_serial")

// AccountDeviceMapping is a row of account_device_mappings. JSON tags
// matter here (unlike a purely-internal repository type) because the
// admin panel's handlers (cmd/api/bss_admin_handlers.go) encode this
// struct directly rather than mapping it into a local response type the
// way cmd/bssadapter's own handlers do.
type AccountDeviceMapping struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	DeviceID    string `json:"device_id"`
	OUISerial   string `json:"oui_serial"`
	ServicePlan string `json:"service_plan,omitempty"`
	Status      string `json:"status"`
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// CreateMapping resolves oui_serial against the real devices table and
// upserts the account/device link (Workflow A in the BSS integration
// guide).
func (r *Repository) CreateMapping(ctx context.Context, accountID, ouiSerial, servicePlan string) (*AccountDeviceMapping, error) {
	var deviceID string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM devices WHERE oui_serial = $1`, ouiSerial).Scan(&deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrDeviceNotFound, ouiSerial)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve device for mapping: %w", err)
	}

	_, err = r.db.ExecContext(ctx, `
		INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, service_plan, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (account_id, device_id) DO UPDATE SET
			service_plan = EXCLUDED.service_plan,
			updated_at = now()
	`, uuid.New().String(), accountID, deviceID, ouiSerial, nullIfEmpty(servicePlan), StatusActive)
	if err != nil {
		return nil, fmt.Errorf("create mapping: %w", err)
	}

	return r.getByAccountDevice(ctx, accountID, deviceID)
}

func (r *Repository) getByAccountDevice(ctx context.Context, accountID, deviceID string) (*AccountDeviceMapping, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, account_id, device_id, oui_serial, service_plan, status
		FROM account_device_mappings WHERE account_id = $1 AND device_id = $2
	`, accountID, deviceID)
	return scanMapping(row)
}

// ListByAccount returns every device mapped to an account.
func (r *Repository) ListByAccount(ctx context.Context, accountID string) ([]AccountDeviceMapping, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, device_id, oui_serial, service_plan, status
		FROM account_device_mappings WHERE account_id = $1
		ORDER BY created_at ASC
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list mappings: %w", err)
	}
	defer rows.Close()

	var out []AccountDeviceMapping
	for rows.Next() {
		m, err := scanMapping(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// PrimaryDeviceForAccount returns the account's most recently mapped
// active device — the mapping an order dispatch resolves against.
// Phase 8b assumes one primary device per account, matching every
// example in the BSS integration guide; an account genuinely managing
// multiple devices needs the order to name a device explicitly, which
// isn't part of this phase's scope.
func (r *Repository) PrimaryDeviceForAccount(ctx context.Context, accountID string) (*AccountDeviceMapping, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, account_id, device_id, oui_serial, service_plan, status
		FROM account_device_mappings
		WHERE account_id = $1 AND status = 'ACTIVE'
		ORDER BY updated_at DESC
		LIMIT 1
	`, accountID)
	return scanMapping(row)
}

// ListAll returns every account-device mapping, newest first — backs the
// admin-panel onboarding/setup view (not part of the BSS-facing API,
// which only exposes ListByAccount since a BSS caller only ever knows its
// own account IDs).
func (r *Repository) ListAll(ctx context.Context, limit int) ([]AccountDeviceMapping, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, device_id, oui_serial, service_plan, status
		FROM account_device_mappings ORDER BY created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list all mappings: %w", err)
	}
	defer rows.Close()

	var out []AccountDeviceMapping
	for rows.Next() {
		m, err := scanMapping(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMapping(s scanner) (*AccountDeviceMapping, error) {
	var m AccountDeviceMapping
	var servicePlan sql.NullString
	if err := s.Scan(&m.ID, &m.AccountID, &m.DeviceID, &m.OUISerial, &servicePlan, &m.Status); err != nil {
		return nil, fmt.Errorf("scan mapping: %w", err)
	}
	if servicePlan.Valid {
		m.ServicePlan = servicePlan.String
	}
	return &m, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
