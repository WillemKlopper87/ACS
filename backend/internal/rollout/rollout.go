// Package rollout owns firmware canary rollouts (build plan §4 Phase 4's
// deferred item, built here in Phase 7 / design doc v3 §9.5). A
// firmware_rollout_device row's per-device state is deliberately NOT a
// column that duplicates the truth — DOWNLOADING/SUCCESS/FAILED are read
// live from the jobs table (the single source of truth for a
// FIRMWARE_DOWNLOAD job's outcome, already correctly handling the
// Download/TransferComplete split from Phase 4) via job_id. This package
// only tracks what's ELIGIBLE (computed once, at rollout creation) versus
// dispatched.
package rollout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	StatusDraft     = "DRAFT"
	StatusActive    = "ACTIVE"
	StatusBlocked   = "BLOCKED"
	StatusCompleted = "COMPLETED"
	StatusAborted   = "ABORTED"
)

// Per-device states (design doc v3 §9.5's vocabulary). PENDING/SKIPPED/
// BLOCKED at the per-device level aren't produced by this pass — every
// device computed eligible at creation starts ELIGIBLE and either stays
// there or gets dispatched; see the package doc for why DOWNLOADING/
// SUCCESS/FAILED are derived from jobs.status, not stored here.
const (
	DeviceStateEligible    = "ELIGIBLE"
	DeviceStateQueued      = "QUEUED"
	DeviceStateDownloading = "DOWNLOADING"
	DeviceStateSuccess     = "SUCCESS"
	DeviceStateFailed      = "FAILED"
)

var (
	ErrNotFound            = errors.New("rollout not found")
	ErrNotDraft            = errors.New("rollout is not in DRAFT status")
	ErrNotActive           = errors.New("rollout is not ACTIVE")
	ErrOutsideMaintenance  = errors.New("current time is outside the configured maintenance window")
	ErrFailureRateExceeded = errors.New("failure rate exceeds maximum_failure_rate; rollout blocked")
	ErrNoEligibleDevices   = errors.New("no eligible devices to dispatch")
)

// Rollout is a row of the firmware_rollout table.
type Rollout struct {
	ID                        string
	Name                      string
	FirmwareImageID           string
	RollbackFirmwareImageID   *string
	ModelFilter               *string
	CurrentVersionFilter      *string
	CanaryPercentage          int
	MaximumFailureRate        float64
	MaintenanceWindowStartUTC *string // "HH:MM:SS" or nil
	MaintenanceWindowEndUTC   *string
	Status                    string
	// CustomerID is the rollout's tenant owner (audit P0.7) -- nil means
	// platform-global, restricted the same way DeviceGroup.CustomerID is.
	// It also bounds eligibility computed at creation (candidate devices
	// outside it are never included) so a scoped operator's rollout can
	// never dispatch firmware to another tenant merely because the model
	// filter matches.
	CustomerID           *string
	CreatedBy            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	RollbackDispatchedAt *time.Time
}

// DeviceStatus is one device's live status within a rollout — device
// info joined with the rollout_device row and (if dispatched) its job's
// current state.
type DeviceStatus struct {
	DeviceID     string
	OUISerial    string
	State        string
	JobID        *string
	CommandKey   *string
	DispatchedAt *time.Time
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const rolloutColumns = `id, name, firmware_image_id, rollback_firmware_image_id,
	model_filter, current_version_filter, canary_percentage, maximum_failure_rate,
	maintenance_window_start_utc, maintenance_window_end_utc, status,
	customer_id, created_by, created_at, updated_at, rollback_dispatched_at`

// Create inserts a DRAFT rollout and computes its eligible device set in
// the same transaction — eligibility (model_filter / current_version_filter
// against devices + the cached SoftwareVersion) is a snapshot taken once
// at creation, not re-evaluated live, so a rollout's target set doesn't
// shift under it while it's running.
//
// customerID additionally bounds eligibility to that customer's devices,
// or (nil) to no customer restriction at all — a platform-global rollout,
// which callers must restrict to superadmin/GlobalAccess operators the
// same way every other control object here does (audit P0.7).
func (r *Repository) Create(ctx context.Context, name, firmwareImageID string, rollbackImageID, modelFilter, currentVersionFilter *string,
	canaryPercentage int, maxFailureRate float64, windowStart, windowEnd, customerID *string, createdBy string) (*Rollout, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create rollout tx: %w", err)
	}
	defer tx.Rollback()

	id := uuid.New().String()
	row := tx.QueryRowContext(ctx, `
		INSERT INTO firmware_rollout (id, name, firmware_image_id, rollback_firmware_image_id,
			model_filter, current_version_filter, canary_percentage, maximum_failure_rate,
			maintenance_window_start_utc, maintenance_window_end_utc, customer_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING `+rolloutColumns,
		id, name, firmwareImageID, rollbackImageID, modelFilter, currentVersionFilter,
		canaryPercentage, maxFailureRate, windowStart, windowEnd, customerID, nullIfEmpty(createdBy))

	ro, err := scanRollout(row)
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO firmware_rollout_device (rollout_id, device_id)
		SELECT $1, d.id FROM devices d
		WHERE ($2::text IS NULL OR d.manufacturer ILIKE $2 OR d.product_class ILIKE $2)
		  AND ($3::text IS NULL OR (
		      SELECT p.parameters->'Device.DeviceInfo.SoftwareVersion'->>'value'
		      FROM device_parameter_cache p WHERE p.device_id = d.id
		  ) = $3)
		  AND ($4::uuid IS NULL OR d.customer_id = $4)`,
		id, modelFilter, currentVersionFilter, customerID); err != nil {
		return nil, fmt.Errorf("compute eligible devices: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create rollout tx: %w", err)
	}
	return ro, nil
}

func (r *Repository) ByID(ctx context.Context, id string) (*Rollout, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+rolloutColumns+` FROM firmware_rollout WHERE id = $1`, id)
	ro, err := scanRollout(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return ro, err
}

func (r *Repository) List(ctx context.Context) ([]Rollout, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+rolloutColumns+` FROM firmware_rollout ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list rollouts: %w", err)
	}
	defer rows.Close()

	var out []Rollout
	for rows.Next() {
		ro, err := scanRollout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ro)
	}
	return out, rows.Err()
}

// SetStatus transitions a rollout's status (DRAFT->ACTIVE, ACTIVE->BLOCKED
// on a failure-rate breach, ACTIVE->COMPLETED once nothing's left
// ELIGIBLE, any->ABORTED).
func (r *Repository) SetStatus(ctx context.Context, id, status string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE firmware_rollout SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("set rollout status: %w", err)
	}
	return nil
}

// SetRollbackDispatched records that rollback firmware downloads have
// been queued for this rollout, so a repeated BLOCKED transition (or a
// retried request) doesn't double-dispatch them.
func (r *Repository) SetRollbackDispatched(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE firmware_rollout SET rollback_dispatched_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("set rollout rollback dispatched: %w", err)
	}
	return nil
}

// EligibleDeviceIDs returns devices in this rollout not yet dispatched
// (job_id IS NULL) — what Start/Advance queue firmware downloads for.
func (r *Repository) EligibleDeviceIDs(ctx context.Context, rolloutID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT device_id FROM firmware_rollout_device
		WHERE rollout_id = $1 AND job_id IS NULL`, rolloutID)
	if err != nil {
		return nil, fmt.Errorf("list eligible rollout devices: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan eligible device id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// TotalDevices returns the size of a rollout's whole eligible pool,
// computed once at creation (build plan §4 Phase 4 firm-up: wave sizing
// needs the *original* pool size, not however many remain undispatched,
// so each wave stays canary_percentage of the same base rather than
// shrinking relative to what's left).
func (r *Repository) TotalDevices(ctx context.Context, rolloutID string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM firmware_rollout_device WHERE rollout_id = $1`, rolloutID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count rollout devices: %w", err)
	}
	return n, nil
}

// SuccessfulDeviceIDs returns devices in this rollout whose dispatched job
// actually completed SUCCESS — the "got the (bad) firmware" set a
// rollback needs to target. Devices whose download FAILED/TIMEOUT never
// received the new firmware, so there's nothing on them to roll back.
func (r *Repository) SuccessfulDeviceIDs(ctx context.Context, rolloutID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT rd.device_id FROM firmware_rollout_device rd
		JOIN jobs j ON j.id = rd.job_id
		WHERE rd.rollout_id = $1 AND j.status = 'SUCCESS'`, rolloutID)
	if err != nil {
		return nil, fmt.Errorf("list successful rollout devices: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan successful rollout device id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MarkDispatched records that a device's firmware download job has been
// created — the row moves from ELIGIBLE (job_id NULL) to dispatched
// (job_id set); its live state from here on is read from jobs.status.
func (r *Repository) MarkDispatched(ctx context.Context, rolloutID, deviceID, jobID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE firmware_rollout_device SET job_id = $3, dispatched_at = now()
		WHERE rollout_id = $1 AND device_id = $2`, rolloutID, deviceID, jobID)
	if err != nil {
		return fmt.Errorf("mark rollout device dispatched: %w", err)
	}
	return nil
}

// DeviceStatuses returns every device in a rollout with its live state —
// ELIGIBLE for undispatched rows, otherwise derived from the joined
// job's current status.
func (r *Repository) DeviceStatuses(ctx context.Context, rolloutID string) ([]DeviceStatus, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT rd.device_id, d.oui_serial, rd.job_id, j.command_key, j.status, rd.dispatched_at
		FROM firmware_rollout_device rd
		JOIN devices d ON d.id = rd.device_id
		LEFT JOIN jobs j ON j.id = rd.job_id
		WHERE rd.rollout_id = $1
		ORDER BY d.oui_serial`, rolloutID)
	if err != nil {
		return nil, fmt.Errorf("list rollout device statuses: %w", err)
	}
	defer rows.Close()

	var out []DeviceStatus
	for rows.Next() {
		var ds DeviceStatus
		var jobID, commandKey, jobStatus sql.NullString
		var dispatchedAt sql.NullTime
		if err := rows.Scan(&ds.DeviceID, &ds.OUISerial, &jobID, &commandKey, &jobStatus, &dispatchedAt); err != nil {
			return nil, fmt.Errorf("scan rollout device status: %w", err)
		}
		if jobID.Valid {
			ds.JobID = &jobID.String
		}
		if commandKey.Valid {
			ds.CommandKey = &commandKey.String
		}
		if dispatchedAt.Valid {
			t := dispatchedAt.Time
			ds.DispatchedAt = &t
		}
		ds.State = deriveState(jobStatus)
		out = append(out, ds)
	}
	return out, rows.Err()
}

// FailureRate computes the failure rate among *terminal* dispatched jobs
// only (SUCCESS or FAILED/TIMEOUT) — still-downloading devices don't
// count against the rate yet, since they haven't resolved either way.
// Returns (rate, terminalCount).
func (r *Repository) FailureRate(ctx context.Context, rolloutID string) (float64, int, error) {
	var succeeded, failed int
	err := r.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE j.status = 'SUCCESS'),
			COUNT(*) FILTER (WHERE j.status IN ('FAILED', 'TIMEOUT'))
		FROM firmware_rollout_device rd
		JOIN jobs j ON j.id = rd.job_id
		WHERE rd.rollout_id = $1`, rolloutID).Scan(&succeeded, &failed)
	if err != nil {
		return 0, 0, fmt.Errorf("compute rollout failure rate: %w", err)
	}
	terminal := succeeded + failed
	if terminal == 0 {
		return 0, 0, nil
	}
	return float64(failed) / float64(terminal), terminal, nil
}

func deriveState(jobStatus sql.NullString) string {
	if !jobStatus.Valid {
		return DeviceStateEligible
	}
	switch jobStatus.String {
	case "QUEUED", "RPC_SENT":
		return DeviceStateQueued
	case "AWAITING_TRANSFER_COMPLETE":
		return DeviceStateDownloading
	case "SUCCESS":
		return DeviceStateSuccess
	case "FAILED", "TIMEOUT":
		return DeviceStateFailed
	default:
		return DeviceStateQueued
	}
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRollout(s scanner) (*Rollout, error) {
	var ro Rollout
	var rollbackImageID, modelFilter, versionFilter sql.NullString
	var windowStart, windowEnd sql.NullString
	var createdBy, customerID sql.NullString
	var rollbackDispatchedAt sql.NullTime

	if err := s.Scan(&ro.ID, &ro.Name, &ro.FirmwareImageID, &rollbackImageID,
		&modelFilter, &versionFilter, &ro.CanaryPercentage, &ro.MaximumFailureRate,
		&windowStart, &windowEnd, &ro.Status, &customerID, &createdBy, &ro.CreatedAt, &ro.UpdatedAt, &rollbackDispatchedAt); err != nil {
		return nil, fmt.Errorf("scan rollout: %w", err)
	}
	if customerID.Valid {
		c := customerID.String
		ro.CustomerID = &c
	}
	if rollbackDispatchedAt.Valid {
		t := rollbackDispatchedAt.Time
		ro.RollbackDispatchedAt = &t
	}
	if rollbackImageID.Valid {
		ro.RollbackFirmwareImageID = &rollbackImageID.String
	}
	if modelFilter.Valid {
		ro.ModelFilter = &modelFilter.String
	}
	if versionFilter.Valid {
		ro.CurrentVersionFilter = &versionFilter.String
	}
	if windowStart.Valid {
		ro.MaintenanceWindowStartUTC = &windowStart.String
	}
	if windowEnd.Valid {
		ro.MaintenanceWindowEndUTC = &windowEnd.String
	}
	if createdBy.Valid {
		ro.CreatedBy = createdBy.String
	}
	return &ro, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// InMaintenanceWindow reports whether t's UTC time-of-day falls within
// [start, end) — start > end is treated as an overnight window (e.g.
// 22:00-06:00) wrapping past midnight. A rollout with no window
// configured (both nil) always returns true.
func InMaintenanceWindow(t time.Time, start, end *string) bool {
	if start == nil || end == nil {
		return true
	}
	s, err1 := time.Parse("15:04:05", *start)
	e, err2 := time.Parse("15:04:05", *end)
	if err1 != nil || err2 != nil {
		return true
	}
	now := t.UTC()
	nowMin := now.Hour()*60 + now.Minute()
	startMin := s.Hour()*60 + s.Minute()
	endMin := e.Hour()*60 + e.Minute()
	if startMin <= endMin {
		return nowMin >= startMin && nowMin < endMin
	}
	return nowMin >= startMin || nowMin < endMin
}
