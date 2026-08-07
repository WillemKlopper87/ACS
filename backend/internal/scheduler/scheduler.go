// Package scheduler owns recurring job definitions (build plan §4 Phase
// 7 / design doc v3 Phase 7: "Scheduled jobs"). A ScheduledJob describes
// *what* to run and *how often*; cmd/api's worker (schedule_worker.go)
// is what actually turns a due one into real jobs.Repository.Create
// calls, the same way connreq_worker turns queued CONNECTION_REQUEST
// jobs into real outbound GETs — this package only owns the definitions
// and their due/not-due state, not the dispatch loop.
package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Target types a scheduled job can point at — either one device directly,
// or every current member of a device_groups row (build plan §4 Phase 7's
// other addition) at dispatch time, not membership frozen at creation.
const (
	TargetDevice = "DEVICE"
	TargetGroup  = "GROUP"
)

var (
	ErrNotFound         = errors.New("scheduled job not found")
	ErrIntervalTooShort = errors.New("interval_seconds must be at least 60")
)

// MinIntervalSeconds guards against an operator fat-fingering a 1-second
// interval and turning this into an accidental flood generator.
const MinIntervalSeconds = 60

// ScheduledJob is a row of the scheduled_jobs table.
type ScheduledJob struct {
	ID              string
	Name            string
	JobType         string
	TargetType      string
	TargetID        string
	Payload         json.RawMessage
	IntervalSeconds int
	Enabled         bool
	NextRunAt       time.Time
	LastRunAt       *time.Time
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const columns = `id, name, job_type, target_type, target_id, payload,
	interval_seconds, enabled, next_run_at, last_run_at, created_by, created_at, updated_at`

// Create schedules a new recurring job, due to run immediately (next_run_at
// = now()) — an operator creating one wants to see it fire on the next
// worker tick, not wait a full interval first.
func (r *Repository) Create(ctx context.Context, name, jobType, targetType, targetID string, payload any, intervalSeconds int, createdBy string) (*ScheduledJob, error) {
	if intervalSeconds < MinIntervalSeconds {
		return nil, ErrIntervalTooShort
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal scheduled job payload: %w", err)
	}

	id := uuid.New().String()
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO scheduled_jobs (id, name, job_type, target_type, target_id, payload, interval_seconds, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+columns,
		id, name, jobType, targetType, targetID, payloadJSON, intervalSeconds, nullIfEmpty(createdBy))

	return scan(row)
}

func (r *Repository) ByID(ctx context.Context, id string) (*ScheduledJob, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM scheduled_jobs WHERE id = $1`, id)
	sj, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sj, err
}

func (r *Repository) List(ctx context.Context) ([]ScheduledJob, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+columns+` FROM scheduled_jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list scheduled jobs: %w", err)
	}
	defer rows.Close()

	var out []ScheduledJob
	for rows.Next() {
		sj, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sj)
	}
	return out, rows.Err()
}

// Delete removes a scheduled job definition — it does not touch jobs
// already created from past runs, those live their own lifecycle in the
// jobs table same as any operator-queued job.
func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM scheduled_jobs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete scheduled job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetEnabled pauses or resumes a scheduled job without losing its
// definition — the common case for "stop this while we investigate
// something" without having to recreate it with the same settings after.
func (r *Repository) SetEnabled(ctx context.Context, id string, enabled bool) (*ScheduledJob, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE scheduled_jobs SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
	if err != nil {
		return nil, fmt.Errorf("set scheduled job enabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return r.ByID(ctx, id)
}

// LeaseDue atomically finds one enabled, due scheduled job and pushes its
// next_run_at forward by its own interval — same FOR UPDATE SKIP LOCKED
// shape jobs.Repository.Lease uses, so multiple worker ticks (or a future
// second cmd/api replica) can't double-fire the same schedule.
func (r *Repository) LeaseDue(ctx context.Context) (*ScheduledJob, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin lease due tx: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		SELECT `+columns+` FROM scheduled_jobs
		WHERE enabled AND next_run_at <= now()
		ORDER BY next_run_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED`)

	sj, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE scheduled_jobs
		SET next_run_at = now() + (interval_seconds || ' seconds')::interval,
			last_run_at = now(), updated_at = now()
		WHERE id = $1`, sj.ID); err != nil {
		return nil, fmt.Errorf("advance scheduled job: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit lease due tx: %w", err)
	}
	return sj, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner) (*ScheduledJob, error) {
	var sj ScheduledJob
	var createdBy sql.NullString
	var lastRunAt sql.NullTime

	if err := s.Scan(&sj.ID, &sj.Name, &sj.JobType, &sj.TargetType, &sj.TargetID, &sj.Payload,
		&sj.IntervalSeconds, &sj.Enabled, &sj.NextRunAt, &lastRunAt, &createdBy, &sj.CreatedAt, &sj.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scan scheduled job: %w", err)
	}
	if createdBy.Valid {
		sj.CreatedBy = createdBy.String
	}
	if lastRunAt.Valid {
		t := lastRunAt.Time
		sj.LastRunAt = &t
	}
	return &sj, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
