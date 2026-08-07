// Package jobs owns the durable ACS-initiated work queue (design doc v3
// §7.4, trimmed to the Phase 2 subset — see
// migrations/0004_jobs.sql). Every ACS-initiated action becomes a job
// here; the CWMP gateway leases and dispatches them serially, one
// in-flight RPC per session (v3 §5.4).
package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	TypeSetParameter           = "SET_PARAMETER"
	TypeGetParameter           = "GET_PARAMETER"
	TypeConnectionRequest      = "CONNECTION_REQUEST"
	TypeFirmwareDownload       = "FIRMWARE_DOWNLOAD"
	TypeDiagnosticsPing        = "DIAGNOSTICS_PING"
	TypeDiagnosticsTraceroute  = "DIAGNOSTICS_TRACEROUTE"
	TypeAddObject              = "ADD_OBJECT"
	TypeDeleteObject           = "DELETE_OBJECT"
	TypeReboot                 = "REBOOT"
	TypeFactoryReset           = "FACTORY_RESET"
	TypeScheduleInform         = "SCHEDULE_INFORM"
	TypeSetParameterAttributes = "SET_PARAMETER_ATTRIBUTES"
	TypeGetParameterAttributes = "GET_PARAMETER_ATTRIBUTES"
	TypeUpload                 = "UPLOAD"
	TypeParameterDiscovery     = "PARAMETER_DISCOVERY"

	StatusQueued                   = "QUEUED"
	StatusRPCSent                  = "RPC_SENT"
	StatusInProgress               = "IN_PROGRESS"
	StatusAwaitingTransferComplete = "AWAITING_TRANSFER_COMPLETE"
	StatusSuccess                  = "SUCCESS"
	StatusFailed                   = "FAILED"
	StatusTimeout                  = "TIMEOUT"
)

// sessionDispatchableTypes are the job types cmd/acs's per-device Lease
// hands out — types that make sense as a CWMP RPC sent within an open
// session. CONNECTION_REQUEST is deliberately excluded: it isn't a CWMP
// RPC at all, and dispatching it to a session would hit
// renderJobRequest's "unrenderable payload" fallback and fail the job
// incorrectly. It has its own cross-device lease (LeaseNextByType) for
// the background connreq worker instead (build plan §4 Phase 3).
// FIRMWARE_DOWNLOAD *is* session-dispatchable — Download itself is a
// normal in-session RPC; only the later TransferComplete is out-of-band
// (build plan §4 Phase 4), and that's handled by CommandKey lookup in
// cmd/acs, not by session leasing.
// DIAGNOSTICS_PING is also session-dispatchable: both its trigger
// (SetParameterValues) and its poll (GetParameterValues) are ordinary
// in-session RPCs — cmd/acs picks which one to render by checking
// job.Attempts (build plan §4 Phase 5).
var sessionDispatchableTypes = []string{
	TypeSetParameter, TypeGetParameter, TypeFirmwareDownload, TypeDiagnosticsPing, TypeDiagnosticsTraceroute,
	TypeAddObject, TypeDeleteObject, TypeReboot, TypeFactoryReset,
	TypeScheduleInform, TypeSetParameterAttributes, TypeGetParameterAttributes, TypeUpload,
	TypeParameterDiscovery,
}

// SetParameterPayload is the payload shape for a SET_PARAMETER job,
// mirroring the REST PUT /devices/{id}/parameters body (design doc v3
// §8.3).
type SetParameterPayload struct {
	Parameters []ParameterWrite `json:"parameters"`
}

type ParameterWrite struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

// GetParameterPayload is the payload shape for a GET_PARAMETER job.
type GetParameterPayload struct {
	Paths []string `json:"paths"`
}

// ConnectionRequestPayload is the payload shape for a CONNECTION_REQUEST
// job, mirroring the REST POST /devices/{id}/connection-request body
// (design doc v3 §8.4).
type ConnectionRequestPayload struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

// FirmwareDownloadPayload is the payload shape for a FIRMWARE_DOWNLOAD
// job (design doc v3 §9.2's Download arguments, resolved from a
// firmware_images row at job-creation time rather than re-resolved at
// dispatch time — the job should download the exact image it was created
// against even if a newer one gets uploaded in between).
type FirmwareDownloadPayload struct {
	FirmwareImageID string `json:"firmware_image_id"`
	FileType        string `json:"file_type"`
	URL             string `json:"url"`
	Username        string `json:"username,omitempty"`
	Password        string `json:"password,omitempty"`
	FileSize        int64  `json:"file_size"`
	TargetFilename  string `json:"target_filename"`
	DelaySeconds    int    `json:"delay_seconds"`
}

// DiagnosticsPingPayload is the payload shape for a DIAGNOSTICS_PING job
// (design doc v3 §10.1's Device.IP.Diagnostics.IPPing input parameters).
// It stays attached to the job across the whole trigger->poll->poll...
// cycle since Requeue never touches payload — only the first (attempt 1)
// dispatch reads it.
type DiagnosticsPingPayload struct {
	Host                string `json:"host"`
	NumberOfRepetitions int    `json:"number_of_repetitions"`
	Timeout             int    `json:"timeout"`
	DataBlockSize       int    `json:"data_block_size"`
	DSCP                int    `json:"dscp"`
}

// DiagnosticsTraceroutePayload is the payload shape for a
// DIAGNOSTICS_TRACEROUTE job (design doc v3 §10.1's sibling diagnostic —
// same TR-181 trigger/poll shape as IPPing, different parameter subtree:
// Device.IP.Diagnostics.TraceRoute.*). Build plan §4 Phase 5's explicitly
// deferred item, built here as "the identical pattern" it was always
// described as.
type DiagnosticsTraceroutePayload struct {
	Host          string `json:"host"`
	NumberOfTries int    `json:"number_of_tries"`
	Timeout       int    `json:"timeout"`
	DataBlockSize int    `json:"data_block_size"`
	DSCP          int    `json:"dscp"`
	MaxHopCount   int    `json:"max_hop_count"`
}

// AddObjectPayload is the payload shape for an ADD_OBJECT job.
// ObjectPath is the parent path ending in "." (e.g. "Device.WiFi.SSID.")
// — the CPE picks the new instance number and returns it, recorded on
// job completion (build plan "critical feature backlog": AddObject/
// DeleteObject were the biggest protocol-completeness gap against an
// off-the-shelf ACS — every prior write path could only edit parameters
// that already existed on the device).
type AddObjectPayload struct {
	ObjectPath string `json:"object_path"`
}

// DeleteObjectPayload is the payload shape for a DELETE_OBJECT job.
// ObjectPath is the full path to the instance being removed, ending in
// "." (e.g. "Device.WiFi.SSID.3.").
type DeleteObjectPayload struct {
	ObjectPath string `json:"object_path"`
}

// RebootPayload and FactoryResetPayload carry no fields — TR-069 gives
// neither RPC any arguments beyond (for Reboot) the CommandKey every job
// already has.
type RebootPayload struct{}
type FactoryResetPayload struct{}

// ScheduleInformPayload is the payload shape for a SCHEDULE_INFORM job.
type ScheduleInformPayload struct {
	DelaySeconds int `json:"delay_seconds"`
}

// AttributeWrite mirrors cwmp.AttributeWrite for JSON payload storage
// (this package doesn't import internal/cwmp, matching the existing
// duplication convention every other payload type here already follows).
type AttributeWrite struct {
	Name         string `json:"name"`
	Notification int    `json:"notification"`
}

// SetParameterAttributesPayload is the payload shape for a
// SET_PARAMETER_ATTRIBUTES job.
type SetParameterAttributesPayload struct {
	Attributes []AttributeWrite `json:"attributes"`
}

// GetParameterAttributesPayload is the payload shape for a
// GET_PARAMETER_ATTRIBUTES job.
type GetParameterAttributesPayload struct {
	Paths []string `json:"paths"`
}

// UploadPayload is the payload shape for an UPLOAD job. FileType follows
// TR-069 §A.3.2.7's enumeration (e.g. "1 Vendor Configuration File", "2
// Vendor Log File"). URL points at cmd/api's upload-receipt endpoint —
// resolved at job-creation time from a fresh per-job token, the same
// "resolve once, not re-resolved at dispatch" reasoning
// FirmwareDownloadPayload already uses for its firmware image URL.
type UploadPayload struct {
	FileType     string `json:"file_type"`
	URL          string `json:"url"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	DelaySeconds int    `json:"delay_seconds"`
}

// ParameterDiscoveryPayload is the payload shape for a PARAMETER_DISCOVERY
// job — a full-tree GetParameterNames(Root, NextLevel=false) sent
// automatically the first time a device connects (BOOTSTRAP), or on demand
// for an already-onboarded device. Root is tried first; if the CPE returns
// a fault or an empty list (the standard signal that a path doesn't exist
// under that root), completeJob chains one fallback job at FallbackRoot
// rather than guessing — IsFallback marks that second attempt so the chain
// stops after one retry instead of looping.
type ParameterDiscoveryPayload struct {
	Root         string `json:"root"`
	FallbackRoot string `json:"fallback_root,omitempty"`
	IsFallback   bool   `json:"is_fallback,omitempty"`
}

// Job is a row of the jobs table.
type Job struct {
	ID           string
	CommandKey   string
	DeviceID     string
	Type         string
	Status       string
	Payload      json.RawMessage
	Attempts     int
	MaxAttempts  int
	CreatedBy    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
	FaultCode    *string
	FaultString  *string
	ResultDetail json.RawMessage
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const jobColumns = `id, command_key, device_id, type, status, payload, attempts, max_attempts,
	created_by, created_at, updated_at, started_at, completed_at, fault_code, fault_string, result_detail`

// Create queues a new job in status QUEUED. payload is marshaled to JSON
// as-is (typically a SetParameterPayload or GetParameterPayload).
func (r *Repository) Create(ctx context.Context, deviceID, jobType string, payload any, createdBy string) (*Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal job payload: %w", err)
	}

	id := uuid.New().String()
	commandKey := newCommandKey(jobType)

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO jobs (id, command_key, device_id, type, status, payload, created_by)
		VALUES ($1, $2, $3, $4, 'QUEUED', $5, $6)
		RETURNING `+jobColumns,
		id, commandKey, deviceID, jobType, payloadJSON, nullIfEmpty(createdBy))

	return scanJob(row)
}

// CreateWithMaxAttempts is Create, but overrides the table's default
// max_attempts (3) — DIAGNOSTICS_PING isn't a fixed-retry RPC like the
// others, it's a poll loop (trigger + repeated GetParameterValues) that
// needs enough attempts to outlast a real ping run without polling
// forever (build plan §4 Phase 5).
func (r *Repository) CreateWithMaxAttempts(ctx context.Context, deviceID, jobType string, payload any, createdBy string, maxAttempts int) (*Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal job payload: %w", err)
	}

	id := uuid.New().String()
	commandKey := newCommandKey(jobType)

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO jobs (id, command_key, device_id, type, status, payload, created_by, max_attempts)
		VALUES ($1, $2, $3, $4, 'QUEUED', $5, $6, $7)
		RETURNING `+jobColumns,
		id, commandKey, deviceID, jobType, payloadJSON, nullIfEmpty(createdBy), maxAttempts)

	return scanJob(row)
}

// Lease atomically finds the oldest QUEUED session-dispatchable job
// (SET_PARAMETER/GET_PARAMETER — see sessionDispatchableTypes) for a
// device and marks it RPC_SENT, so two concurrent gateway instances (or a
// retried request) can never dispatch the same job twice. Returns nil,
// nil if there is no queued work.
func (r *Repository) Lease(ctx context.Context, deviceID string) (*Job, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin lease tx: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		SELECT `+jobColumns+` FROM jobs
		WHERE device_id = $1 AND status = 'QUEUED' AND type = ANY($2)
		ORDER BY created_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`, deviceID, sessionDispatchableTypes)

	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET status = 'RPC_SENT', started_at = now(), updated_at = now(), attempts = attempts + 1
		WHERE id = $1
	`, job.ID); err != nil {
		return nil, fmt.Errorf("mark job leased: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit lease tx: %w", err)
	}

	job.Status = StatusRPCSent
	job.Attempts++
	return job, nil
}

// LeaseNextByType atomically finds the oldest QUEUED job of the given
// type across *all* devices and marks it IN_PROGRESS. This is the
// cross-device counterpart to Lease, for job types that aren't triggered
// by an inbound CWMP request — currently just CONNECTION_REQUEST, whose
// background worker (cmd/api) has to go looking for work rather than
// waiting for a device to check in. Returns nil, nil if there is none.
func (r *Repository) LeaseNextByType(ctx context.Context, jobType string) (*Job, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin lease tx: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		SELECT `+jobColumns+` FROM jobs
		WHERE status = 'QUEUED' AND type = $1
		ORDER BY created_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`, jobType)

	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET status = 'IN_PROGRESS', started_at = now(), updated_at = now(), attempts = attempts + 1
		WHERE id = $1
	`, job.ID); err != nil {
		return nil, fmt.Errorf("mark job leased: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit lease tx: %w", err)
	}

	job.Status = StatusInProgress
	job.Attempts++
	return job, nil
}

// MarkSuccess completes a job successfully.
func (r *Repository) MarkSuccess(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'SUCCESS', completed_at = now(), updated_at = now()
		WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("mark job success: %w", err)
	}
	return nil
}

// MarkSuccessWithDetail is MarkSuccess plus an arbitrary result payload —
// e.g. ADD_OBJECT's CPE-assigned InstanceNumber, which has nowhere else
// to live (unlike SET_PARAMETER's result, which lands in the parameter
// cache via a confirmation read).
func (r *Repository) MarkSuccessWithDetail(ctx context.Context, id string, detail any) error {
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("marshal job result detail: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'SUCCESS', completed_at = now(), updated_at = now(), result_detail = $2
		WHERE id = $1
	`, id, detailJSON)
	if err != nil {
		return fmt.Errorf("mark job success with detail: %w", err)
	}
	return nil
}

// MarkFailed completes a job with a CWMP fault.
func (r *Repository) MarkFailed(ctx context.Context, id, faultCode, faultString string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'FAILED', completed_at = now(), updated_at = now(),
			fault_code = $2, fault_string = $3
		WHERE id = $1
	`, id, faultCode, faultString)
	if err != nil {
		return fmt.Errorf("mark job failed: %w", err)
	}
	return nil
}

// MarkTimeout completes a job as TIMEOUT — distinct from FAILED: the
// Connection Request GET itself succeeded (the CPE is reachable), but no
// Inform arrived within the wait window (design doc v3 §12.4's CGNAT
// case).
func (r *Repository) MarkTimeout(ctx context.Context, id, detail string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'TIMEOUT', completed_at = now(), updated_at = now(), fault_string = $2
		WHERE id = $1
	`, id, detail)
	if err != nil {
		return fmt.Errorf("mark job timeout: %w", err)
	}
	return nil
}

// MarkAwaitingTransferComplete records that the CPE accepted a Download
// request (DownloadResponse received) but the transfer itself hasn't
// finished yet — distinct from SUCCESS on purpose (v3 §9.2/§19.7: an
// accepted Download is not a completed one).
func (r *Repository) MarkAwaitingTransferComplete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'AWAITING_TRANSFER_COMPLETE', updated_at = now()
		WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("mark job awaiting transfer complete: %w", err)
	}
	return nil
}

// Requeue cycles a job back to QUEUED without touching attempts (Lease
// increments that itself on the next pickup) or finalizing it as
// SUCCESS/FAILED. This is DIAGNOSTICS_PING's poll loop: the same
// job/CommandKey moves trigger -> poll -> poll -> ... -> terminal instead
// of spawning a new job per poll (build plan §4 Phase 5).
func (r *Repository) Requeue(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'QUEUED', updated_at = now()
		WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("requeue job: %w", err)
	}
	return nil
}

// ByID fetches a job by its internal ID (used by the session dispatcher,
// which tracks the in-flight job via cwmp_sessions.current_job_id).
func (r *Repository) ByID(ctx context.Context, id string) (*Job, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = $1`, id)
	return scanJob(row)
}

// ByCommandKey fetches a job by its operator-facing CommandKey (REST job
// status endpoint, design doc v3 §8.5).
func (r *Repository) ByCommandKey(ctx context.Context, commandKey string) (*Job, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE command_key = $1`, commandKey)
	return scanJob(row)
}

// StatusCountsSince returns job counts by status, for jobs created at or
// after since — Fleet Health's "RPC fault rate" signal (design doc v3
// §16.1), computed over a real recent window rather than all-time so a
// fleet that used to be unhealthy but has since recovered isn't dragged
// down by history.
func (r *Repository) StatusCountsSince(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM jobs WHERE created_at >= $1 GROUP BY status`, since)
	if err != nil {
		return nil, fmt.Errorf("count job statuses since: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan job status count: %w", err)
		}
		out[status] = count
	}
	return out, rows.Err()
}

// listLimit caps List's result size. There's no pagination yet (v3 §8.1's
// page/page_size pattern is a REST API concern to add when a real fleet's
// job volume needs it) — a hard cap is the simplest thing that keeps a
// Jobs screen from issuing an unbounded query in the meantime.
const listLimit = 200

// List returns the most recently created jobs, optionally filtered to one
// device. deviceID == "" lists across the whole fleet (the Jobs screen);
// a non-empty deviceID scopes it to one device (Device Detail's recent
// jobs panel).
func (r *Repository) List(ctx context.Context, deviceID string) ([]Job, error) {
	var rows *sql.Rows
	var err error
	if deviceID == "" {
		rows, err = r.db.QueryContext(ctx, `
			SELECT `+jobColumns+` FROM jobs ORDER BY created_at DESC LIMIT $1
		`, listLimit)
	} else {
		rows, err = r.db.QueryContext(ctx, `
			SELECT `+jobColumns+` FROM jobs WHERE device_id = $1 ORDER BY created_at DESC LIMIT $2
		`, deviceID, listLimit)
	}
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(s scanner) (*Job, error) {
	var j Job
	var createdBy sql.NullString
	var startedAt, completedAt sql.NullTime
	var faultCode, faultString sql.NullString
	var resultDetail []byte

	if err := s.Scan(&j.ID, &j.CommandKey, &j.DeviceID, &j.Type, &j.Status, &j.Payload,
		&j.Attempts, &j.MaxAttempts, &createdBy, &j.CreatedAt, &j.UpdatedAt,
		&startedAt, &completedAt, &faultCode, &faultString, &resultDetail); err != nil {
		return nil, fmt.Errorf("scan job: %w", err)
	}
	if resultDetail != nil {
		j.ResultDetail = resultDetail
	}

	if createdBy.Valid {
		j.CreatedBy = createdBy.String
	}
	if startedAt.Valid {
		t := startedAt.Time
		j.StartedAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time
		j.CompletedAt = &t
	}
	if faultCode.Valid {
		j.FaultCode = &faultCode.String
	}
	if faultString.Valid {
		j.FaultString = &faultString.String
	}
	return &j, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// newCommandKey generates an operator-facing job identifier in the shape
// v3's examples use throughout §8/§9 (e.g. "setparam_20260804_0001") —
// randomized rather than sequence-counted to avoid a contended counter
// under concurrent job creation.
func newCommandKey(jobType string) string {
	prefix := "job"
	switch jobType {
	case TypeSetParameter:
		prefix = "setparam"
	case TypeGetParameter:
		prefix = "getparam"
	case TypeConnectionRequest:
		prefix = "cr"
	case TypeFirmwareDownload:
		prefix = "fw"
	case TypeDiagnosticsPing:
		prefix = "diag"
	case TypeDiagnosticsTraceroute:
		prefix = "trace"
	case TypeAddObject:
		prefix = "addobj"
	case TypeDeleteObject:
		prefix = "delobj"
	case TypeReboot:
		prefix = "reboot"
	case TypeFactoryReset:
		prefix = "reset"
	case TypeScheduleInform:
		prefix = "schedinform"
	case TypeSetParameterAttributes:
		prefix = "setattr"
	case TypeGetParameterAttributes:
		prefix = "getattr"
	case TypeUpload:
		prefix = "upload"
	case TypeParameterDiscovery:
		prefix = "discover"
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%s_%s", prefix, time.Now().UTC().Format("20060102"), hex.EncodeToString(b))
}
