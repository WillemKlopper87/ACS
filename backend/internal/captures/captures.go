// Package captures implements on-demand session capture (design
// docs/superpowers/specs/2026-09-14-session-capture-design.md): a
// device's, an expected identity's, or a remote address's CWMP/USP
// session traffic, redacted, recorded to Postgres for operator
// troubleshooting. No HTTP handler here and no direct dependency on
// cmd/acs/cmd/uspc/cmd/api -- this is the shared repository all three
// call into.
package captures

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"acs/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MatchDevice   = "device"
	MatchIdentity = "identity"
	MatchRemoteIP = "remote_ip"

	StatusActive  = "ACTIVE"
	StatusStopped = "STOPPED"
	StatusExpired = "EXPIRED"
)

// ErrAlreadyActive is returned by Start when an ACTIVE session already
// exists for the same (match_type, match_value) -- the partial unique
// index turning that into a database guarantee, not application
// discipline.
var ErrAlreadyActive = errors.New("an active capture already exists for this target")

// ErrNotFound is returned by Get/Stop for an unknown session id.
var ErrNotFound = errors.New("no capture session with that id")

type Session struct {
	ID         string
	DeviceID   *string
	MatchType  string
	MatchValue string
	Protocol   string
	Status     string
	StartedBy  string
	StartedAt  time.Time
	StoppedAt  *time.Time
	ExpiresAt  time.Time
}

// EffectiveStatus reports what the console should display: an ACTIVE
// row whose expiry has already passed reads as EXPIRED even though no
// background job has flipped its stored status yet (design §7 -- the
// per-event check already treats it as inert via expires_at, so no
// sweep is needed for correctness, only for this display).
func (s Session) EffectiveStatus(now time.Time) string {
	if s.Status == StatusActive && now.After(s.ExpiresAt) {
		return StatusExpired
	}
	return s.Status
}

type Event struct {
	ID         string
	SessionID  string
	Seq        int
	Direction  string
	Kind       string
	OccurredAt time.Time
	Summary    string
	Body       *string
}

type StartParams struct {
	DeviceID    *string // set only for MatchDevice, where it's already known
	MatchType   string
	MatchValue  string
	Protocol    string
	StartedBy   string
	MaxDuration time.Duration
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

const sessionColumns = `id, device_id, match_type, match_value, protocol, status, started_by, started_at, stopped_at, expires_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanSession(s scanner) (*Session, error) {
	var sess Session
	var deviceID sql.NullString
	var stoppedAt sql.NullTime
	if err := s.Scan(&sess.ID, &deviceID, &sess.MatchType, &sess.MatchValue, &sess.Protocol,
		&sess.Status, &sess.StartedBy, &sess.StartedAt, &stoppedAt, &sess.ExpiresAt); err != nil {
		return nil, fmt.Errorf("scan capture session: %w", err)
	}
	if deviceID.Valid {
		sess.DeviceID = &deviceID.String
	}
	if stoppedAt.Valid {
		sess.StoppedAt = &stoppedAt.Time
	}
	return &sess, nil
}

// Start creates a new ACTIVE capture session. Returns ErrAlreadyActive
// if one already exists for the same (match_type, match_value).
func (r *Repository) Start(ctx context.Context, p StartParams) (*Session, error) {
	id := uuid.New().String()
	now := time.Now().UTC()
	expiresAt := now.Add(p.MaxDuration)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin capture session: %w", err)
	}
	defer tx.Rollback()

	// Expiry is authoritative even before the retention sweep runs. Flip
	// an elapsed row inside the same transaction as the insert so the
	// partial ACTIVE uniqueness index does not lock this target out until
	// retention (or forever when retention is disabled).
	if _, err := tx.ExecContext(ctx, `
		UPDATE capture_sessions
		SET status = 'EXPIRED'
		WHERE match_type = $1 AND match_value = $2
		  AND status = 'ACTIVE' AND expires_at <= $3`, p.MatchType, p.MatchValue, now); err != nil {
		return nil, fmt.Errorf("expire prior capture session: %w", err)
	}

	row := tx.QueryRowContext(ctx, `
		INSERT INTO capture_sessions (id, device_id, match_type, match_value, protocol, status, started_by, started_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, 'ACTIVE', $6, $7, $8)
		RETURNING `+sessionColumns,
		id, p.DeviceID, p.MatchType, p.MatchValue, p.Protocol, p.StartedBy, now, expiresAt)
	sess, err := scanSession(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyActive
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyActive
		}
		return nil, fmt.Errorf("commit capture session: %w", err)
	}
	return sess, nil
}

// Stop marks a session STOPPED. A no-op (not an error) if it is already
// non-ACTIVE.
func (r *Repository) Stop(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE capture_sessions SET status = 'STOPPED', stopped_at = now() WHERE id = $1 AND status = 'ACTIVE'`, id)
	return err
}

// Get looks up one session by id.
func (r *Repository) Get(ctx context.Context, id string) (*Session, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM capture_sessions WHERE id = $1`, id)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sess, err
}

// List returns every capture session, newest first.
func (r *Repository) List(ctx context.Context) ([]Session, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+sessionColumns+` FROM capture_sessions ORDER BY started_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list capture sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ListAccessible returns the sessions a scoped operator may inspect:
// resolved sessions belonging to one of their customers, plus unresolved
// sessions they started themselves. An empty customerIDs slice remains
// restrictive. Callers with global access should use List instead.
func (r *Repository) ListAccessible(ctx context.Context, startedBy string, customerIDs []string) ([]Session, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT c.id, c.device_id, c.match_type, c.match_value, c.protocol, c.status,
		       c.started_by, c.started_at, c.stopped_at, c.expires_at
		FROM capture_sessions c
		LEFT JOIN devices d ON d.id = c.device_id
		WHERE (c.device_id IS NULL AND c.started_by = $1)
		   OR d.customer_id::text = ANY($2)
		ORDER BY c.started_at DESC`, startedBy, store.StringArray(customerIDs))
	if err != nil {
		return nil, fmt.Errorf("list accessible capture sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ActiveMatch returns every ACTIVE, unexpired session matching this
// request: MatchDevice/MatchIdentity sessions whose match_value equals
// naturalKey (deliberately the same lookup for both modes -- see the
// migration's own comment), plus MatchRemoteIP sessions whose
// match_value equals remoteIP. Either naturalKey or remoteIP may be
// empty (a caller with no resolved identity yet passes "" for
// naturalKey; USP's outbound dispatch, always device-scoped, has no
// remote address to check and passes "" for remoteIP).
func (r *Repository) ActiveMatch(ctx context.Context, protocol, naturalKey, remoteIP string) ([]Session, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+sessionColumns+` FROM capture_sessions
		WHERE protocol = $1 AND status = 'ACTIVE' AND expires_at > now()
		AND (
			(match_type IN ('device','identity') AND match_value = $2 AND $2 <> '')
			OR (match_type = 'remote_ip' AND match_value = $3 AND $3 <> '')
		)`, protocol, naturalKey, remoteIP)
	if err != nil {
		return nil, fmt.Errorf("active capture match: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ResolveDeviceID backfills device_id on an 'identity' (or 'remote_ip')
// session once the device it belongs to has actually authenticated
// (design §4) -- a no-op if device_id is already set.
func (r *Repository) ResolveDeviceID(ctx context.Context, sessionID, deviceID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE capture_sessions SET device_id = $2 WHERE id = $1 AND device_id IS NULL`, sessionID, deviceID)
	return err
}

// RecordEvent appends one event to a session, seq auto-assigned as
// max(seq)+1 for that session (starting at 1). body is nil for an event
// with nothing worth attaching (e.g. a bare dispatch trigger); callers
// are responsible for having already redacted it.
func (r *Repository) RecordEvent(ctx context.Context, sessionID, direction, kind, summary string, body *string) error {
	id := uuid.New().String()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO capture_events (id, session_id, seq, direction, kind, summary, body)
		VALUES ($1, $2, COALESCE((SELECT MAX(seq) FROM capture_events WHERE session_id = $2), 0) + 1, $3, $4, $5, $6)`,
		id, sessionID, direction, kind, summary, body)
	return err
}

// ListEvents returns a session's events in seq order.
func (r *Repository) ListEvents(ctx context.Context, sessionID string) ([]Event, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, session_id, seq, direction, kind, occurred_at, summary, body
		FROM capture_events WHERE session_id = $1 ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list capture events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var body sql.NullString
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Seq, &e.Direction, &e.Kind, &e.OccurredAt, &e.Summary, &body); err != nil {
			return nil, fmt.Errorf("scan capture event: %w", err)
		}
		if body.Valid {
			e.Body = &body.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
