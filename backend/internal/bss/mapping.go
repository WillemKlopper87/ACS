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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	StatusPendingActive = "PENDING_ACTIVE"
	StatusActive        = "ACTIVE"
	StatusSuspended     = "SUSPENDED"
	StatusTerminated    = "TERMINATED"
)

// Roles describe what a device does for an account, not what the device
// is — the same model can be a gateway at one address and an extender at
// another. Kept in sync with the CHECK constraint in migration 0052.
const (
	RoleGateway  = "gateway"
	RoleONT      = "ont"
	RoleExtender = "extender"
	RoleSTB      = "stb"
	RoleATA      = "ata"
	RoleOther    = "other"
)

// Unassign reasons record why an assignment ended. Kept in sync with the
// CHECK constraint in migration 0052.
const (
	ReasonRMA       = "rma"
	ReasonUpgrade   = "upgrade"
	ReasonReturn    = "return"
	ReasonMoved     = "moved"
	ReasonCorrected = "corrected"
)

// ErrDeviceNotFound is returned when a mapping request's oui_serial
// doesn't match any known device. The reference internal_bss_adapter.go
// draft's RegisterDeviceMapping accepted any device_uuid/oui_serial
// without checking — this is the validation it was missing, and the
// reason mapping creation needs a devices table lookup rather than just
// writing whatever the caller sent.
var ErrDeviceNotFound = errors.New("no device found for oui_serial")

// ErrNoDeviceForRole is returned when an account has no active device in
// the requested role. It is a typed error rather than sql.ErrNoRows so a
// caller can distinguish "no such device" from any other query failure.
var ErrNoDeviceForRole = errors.New("no active device assigned in that role")

// ErrRoleAlreadyAssigned is returned when an account already has an active
// device in the requested role, or the same device is already active for
// the account. Both are unique-index violations; the caller must
// UnassignDevice or SwapDevice first.
var ErrRoleAlreadyAssigned = errors.New("account already has an active device in that role")

// isUniqueViolation reports whether err is Postgres 23505, the code both
// partial unique indexes raise. Same shape as internal/operators.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// AccountDeviceMapping is a row of account_device_mappings. JSON tags
// matter here (unlike a purely-internal repository type) because the
// admin panel's handlers (cmd/api/bss_admin_handlers.go) encode this
// struct directly rather than mapping it into a local response type the
// way cmd/bssadapter's own handlers do.
//
// An assignment is current while UnassignedAt is nil and historical once
// it is set. Status is retained for API compatibility and is NOT what
// decides whether a device currently serves an account (spec §5.4).
type AccountDeviceMapping struct {
	ID             string     `json:"id"`
	AccountID      string     `json:"account_id"`
	DeviceID       string     `json:"device_id"`
	OUISerial      string     `json:"oui_serial"`
	ServicePlan    string     `json:"service_plan,omitempty"`
	Status         string     `json:"status"`
	Role           string     `json:"role"`
	AssignedAt     time.Time  `json:"assigned_at"`
	UnassignedAt   *time.Time `json:"unassigned_at,omitempty"`
	UnassignReason string     `json:"unassign_reason,omitempty"`
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// resolveDeviceID turns an oui_serial into a devices.id, or ErrDeviceNotFound.
// q is either the pool or a transaction so AssignDevice and SwapDevice share it.
func resolveDeviceID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ouiSerial string) (string, error) {
	var deviceID string
	err := q.QueryRowContext(ctx, `SELECT id FROM devices WHERE oui_serial = $1`, ouiSerial).Scan(&deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrDeviceNotFound, ouiSerial)
	}
	if err != nil {
		return "", fmt.Errorf("resolve device: %w", err)
	}
	return deviceID, nil
}

// insertAssignment writes one active assignment row. It is a plain INSERT,
// not an upsert: the partial unique indexes decide whether it is allowed,
// and a violation surfaces as ErrRoleAlreadyAssigned.
func insertAssignment(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, id, accountID, deviceID, ouiSerial, role string, servicePlan any) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, service_plan, status, role)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, id, accountID, deviceID, ouiSerial, servicePlan, StatusActive, role)
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: account %s role %s", ErrRoleAlreadyAssigned, accountID, role)
	}
	if err != nil {
		return fmt.Errorf("insert assignment: %w", err)
	}
	return nil
}

// AssignDevice resolves oui_serial against the devices table and records
// that the device now serves the account in the given role.
func (r *Repository) AssignDevice(ctx context.Context, accountID, ouiSerial, role, servicePlan string) (*AccountDeviceMapping, error) {
	deviceID, err := resolveDeviceID(ctx, r.db, ouiSerial)
	if err != nil {
		return nil, err
	}
	if err := insertAssignment(ctx, r.db, uuid.New().String(), accountID, deviceID, ouiSerial, role, nullIfEmpty(servicePlan)); err != nil {
		return nil, err
	}
	return r.getByAccountDevice(ctx, accountID, deviceID)
}

// UnassignDevice ends the account's current assignment in the given role,
// recording why. The row stays as history.
func (r *Repository) UnassignDevice(ctx context.Context, accountID, role, reason string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE account_device_mappings
		   SET unassigned_at = now(), unassign_reason = $3, updated_at = now()
		 WHERE account_id = $1 AND role = $2 AND unassigned_at IS NULL
	`, accountID, role, reason)
	if err != nil {
		return fmt.Errorf("unassign device: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: account %s role %s", ErrNoDeviceForRole, accountID, role)
	}
	return nil
}

// SwapDevice replaces the device serving an account in a role: close the
// old assignment, then open the new one, in one transaction.
//
// The order and the transaction are both load-bearing. The role-unique
// index rejects the insert while the old row is active, so close must
// come first; and if the insert then fails, the close must roll back or
// the account is left with no device in that role at all -- worse than
// the addressing defect this model exists to fix. The service plan
// carries over to the replacement.
func (r *Repository) SwapDevice(ctx context.Context, accountID, role, newOUISerial, reason string) (*AccountDeviceMapping, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin swap: %w", err)
	}
	defer tx.Rollback()

	var servicePlan sql.NullString
	err = tx.QueryRowContext(ctx, `
		UPDATE account_device_mappings
		   SET unassigned_at = now(), unassign_reason = $3, updated_at = now()
		 WHERE account_id = $1 AND role = $2 AND unassigned_at IS NULL
		 RETURNING service_plan
	`, accountID, role, reason).Scan(&servicePlan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: account %s role %s", ErrNoDeviceForRole, accountID, role)
	}
	if err != nil {
		return nil, fmt.Errorf("release old assignment: %w", err)
	}

	newDeviceID, err := resolveDeviceID(ctx, tx, newOUISerial)
	if err != nil {
		return nil, err // deferred Rollback restores the old assignment
	}
	var plan any
	if servicePlan.Valid {
		plan = servicePlan.String
	}
	if err := insertAssignment(ctx, tx, uuid.New().String(), accountID, newDeviceID, newOUISerial, role, plan); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit swap: %w", err)
	}
	return r.getByAccountDevice(ctx, accountID, newDeviceID)
}

func (r *Repository) getByAccountDevice(ctx context.Context, accountID, deviceID string) (*AccountDeviceMapping, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+mappingColumns+" FROM account_device_mappings WHERE account_id = $1 AND device_id = $2 AND unassigned_at IS NULL",
		accountID, deviceID)
	return scanMapping(row)
}

// ListByAccount returns every device *currently* assigned to an account.
func (r *Repository) ListByAccount(ctx context.Context, accountID string) ([]AccountDeviceMapping, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+mappingColumns+" FROM account_device_mappings WHERE account_id = $1 AND unassigned_at IS NULL ORDER BY assigned_at ASC, id ASC",
		accountID)
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

// ListAll returns every currently assigned account-device mapping, newest
// first — backs the admin-panel onboarding/setup view (not part of the
// BSS-facing API, which only exposes ListByAccount since a BSS caller only
// ever knows its own account IDs).
func (r *Repository) ListAll(ctx context.Context, limit int) ([]AccountDeviceMapping, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+mappingColumns+" FROM account_device_mappings WHERE unassigned_at IS NULL ORDER BY assigned_at DESC LIMIT $1",
		limit)
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

// ActiveDeviceForAccount resolves the one device currently serving an
// account in the given role — the mapping an order dispatches against.
//
// This has exactly one rule and no tiebreak. The partial unique index
// account_device_mappings_active_role_idx guarantees at most one active
// row per (account, role), so there is nothing to order by and no LIMIT 1
// hiding a multiplicity. An unfilled role is ErrNoDeviceForRole.
func (r *Repository) ActiveDeviceForAccount(ctx context.Context, accountID, role string) (*AccountDeviceMapping, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+mappingColumns+" FROM account_device_mappings WHERE account_id = $1 AND role = $2 AND unassigned_at IS NULL",
		accountID, role)
	m, err := scanMapping(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: account %s role %s", ErrNoDeviceForRole, accountID, role)
	}
	return m, err
}

// AssignmentHistory returns every assignment an account has ever had,
// current and released, oldest first — the care-agent view that answers
// "this started after you swapped my router".
func (r *Repository) AssignmentHistory(ctx context.Context, accountID string) ([]AccountDeviceMapping, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+mappingColumns+" FROM account_device_mappings WHERE account_id = $1 ORDER BY assigned_at ASC, id ASC",
		accountID)
	if err != nil {
		return nil, fmt.Errorf("assignment history: %w", err)
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

// mappingColumns is every column scanMapping reads, in scan order. Every
// SELECT against account_device_mappings uses it so the two cannot drift.
const mappingColumns = `id, account_id, device_id, oui_serial, service_plan, status, role, assigned_at, unassigned_at, unassign_reason`

func scanMapping(s scanner) (*AccountDeviceMapping, error) {
	var m AccountDeviceMapping
	var servicePlan, reason sql.NullString
	var unassigned sql.NullTime
	if err := s.Scan(&m.ID, &m.AccountID, &m.DeviceID, &m.OUISerial, &servicePlan, &m.Status,
		&m.Role, &m.AssignedAt, &unassigned, &reason); err != nil {
		return nil, fmt.Errorf("scan mapping: %w", err)
	}
	if servicePlan.Valid {
		m.ServicePlan = servicePlan.String
	}
	if unassigned.Valid {
		t := unassigned.Time
		m.UnassignedAt = &t
	}
	if reason.Valid {
		m.UnassignReason = reason.String
	}
	return &m, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
