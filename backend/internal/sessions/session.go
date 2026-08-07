// Package sessions tracks CWMP session lifecycle durably in Postgres
// (design doc v3 §7.3, trimmed to the Phase 1 subset — see
// migrations/0002_cwmp_sessions.sql). Full session timers and the
// serial-RPC-dispatch state machine (v3 §5) are Phase 2+; Phase 1 only
// needs to know a session is open and record when/why it closed.
package sessions

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"acs/internal/store"
)

const (
	StateInformReceived     = "INFORM_RECEIVED"
	StateInformResponseSent = "INFORM_RESPONSE_SENT"
	StateReadyForDispatch   = "READY_FOR_RPC_DISPATCH"
	StateRPCDispatched      = "RPC_DISPATCHED"
	StateClosed             = "SESSION_CLOSED"
)

type Session struct {
	ID               string
	DeviceID         string
	State            string
	InformEventCodes []string
	CurrentJobID     *string
	OpenedAt         time.Time
	ClosedAt         *time.Time
	CloseReason      *string
}

// IsClosed reports whether this session has already been closed.
func (s Session) IsClosed() bool {
	return s.ClosedAt != nil
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Open records a new session in state INFORM_RESPONSE_SENT (the ACS has
// received Inform and is about to answer it — by the time the caller
// has a Session to hand back, the response is already on its way).
func (r *Repository) Open(ctx context.Context, deviceID string, eventCodes []string) (*Session, error) {
	id := uuid.New().String()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO cwmp_sessions (id, device_id, state, inform_event_codes, opened_at)
		VALUES ($1, $2, $3, $4, now())
	`, id, deviceID, StateInformResponseSent, store.StringArray(eventCodes))
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}
	return r.Get(ctx, id)
}

func (r *Repository) Get(ctx context.Context, id string) (*Session, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, device_id, state, inform_event_codes, current_job_id, opened_at, closed_at, close_reason
		FROM cwmp_sessions WHERE id = $1
	`, id)

	var s Session
	var currentJobID sql.NullString
	var closedAt sql.NullTime
	var closeReason sql.NullString
	var eventCodes store.StringArray
	if err := row.Scan(&s.ID, &s.DeviceID, &s.State, &eventCodes, &currentJobID, &s.OpenedAt, &closedAt, &closeReason); err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	if currentJobID.Valid {
		s.CurrentJobID = &currentJobID.String
	}
	if closedAt.Valid {
		t := closedAt.Time
		s.ClosedAt = &t
	}
	if closeReason.Valid {
		s.CloseReason = &closeReason.String
	}
	s.InformEventCodes = []string(eventCodes)
	return &s, nil
}

// SetCurrentJob records which job's RPC this session is now waiting on
// (or clears it, when jobID is ""), and advances the session state to
// match — this is the Postgres-backed equivalent of the Phase 0 in-memory
// ProbeSession.current field (design doc v3 §5.4 one-in-flight-RPC model).
func (r *Repository) SetCurrentJob(ctx context.Context, sessionID, jobID string) error {
	state := StateReadyForDispatch
	var jobIDArg any
	if jobID != "" {
		state = StateRPCDispatched
		jobIDArg = jobID
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE cwmp_sessions SET current_job_id = $2, state = $3
		WHERE id = $1
	`, sessionID, jobIDArg, state)
	if err != nil {
		return fmt.Errorf("set session current job: %w", err)
	}
	return nil
}

// Close marks a session closed. reason is a short machine-readable tag
// (e.g. "NO_PENDING_RPCS") — Phase 1 always closes with that reason since
// there is no job queue yet to have pending work (Phase 2, build plan §4).
func (r *Repository) Close(ctx context.Context, id, reason string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE cwmp_sessions SET state = $2, closed_at = now(), close_reason = $3
		WHERE id = $1
	`, id, StateClosed, reason)
	if err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	return nil
}

// IsOpen reports whether the session exists and has not been closed.
func (r *Repository) IsOpen(ctx context.Context, id string) (bool, string, error) {
	var deviceID string
	var closedAt sql.NullTime
	err := r.db.QueryRowContext(ctx,
		`SELECT device_id, closed_at FROM cwmp_sessions WHERE id = $1`, id,
	).Scan(&deviceID, &closedAt)
	if err == sql.ErrNoRows {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("check session: %w", err)
	}
	return !closedAt.Valid, deviceID, nil
}
