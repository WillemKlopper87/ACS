// Leasing, lease heartbeat, and stale-lease recovery (split out of
// job.go, audit P1.1/P3.1).
package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// Lease durations (audit P1.1). A session-dispatched RPC completes
// within one CWMP session, so its lease is short; connection requests
// are bounded by their own timeout; a firmware transfer waiting on
// TransferComplete may legitimately take hours, so it has a generous
// separate deadline rather than a lease. Expired leases are recovered
// by RecoverExpiredLeases.
const (
	sessionLease     = 15 * time.Minute
	workerLease      = 5 * time.Minute
	transferDeadline = 24 * time.Hour
)

// LeaseOwner identifies this process on every lease it takes, so an
// operator inspecting a stranded job can tell which instance held it.
var LeaseOwner = func() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s/%d", host, os.Getpid())
}()

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
		UPDATE jobs SET status = 'RPC_SENT', started_at = now(), updated_at = now(), attempts = attempts + 1,
			lease_owner = $2, leased_until = now() + $3 * interval '1 second'
		WHERE id = $1
	`, job.ID, LeaseOwner, int(sessionLease.Seconds())); err != nil {
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
		UPDATE jobs SET status = 'IN_PROGRESS', started_at = now(), updated_at = now(), attempts = attempts + 1,
			lease_owner = $2, leased_until = now() + $3 * interval '1 second'
		WHERE id = $1
	`, job.ID, LeaseOwner, int(workerLease.Seconds())); err != nil {
		return nil, fmt.Errorf("mark job leased: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit lease tx: %w", err)
	}

	job.Status = StatusInProgress
	job.Attempts++
	return job, nil
}

// ExtendLease pushes a leased job's deadline out by d from now — the
// heartbeat for work that legitimately outlives its initial lease.
func (r *Repository) ExtendLease(ctx context.Context, id string, d time.Duration) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET leased_until = now() + $2 * interval '1 second', updated_at = now()
		WHERE id = $1 AND status IN ('RPC_SENT', 'IN_PROGRESS')
	`, id, int(d.Seconds()))
	if err != nil {
		return fmt.Errorf("extend job lease: %w", err)
	}
	return nil
}

// nonRepeatableTypes are RPCs the protocol document (audit P1.7) singles
// out for "particular attention": a lease expiring after dispatch does
// not prove the CPE never received or executed the RPC, only that this
// process lost track of the outcome. For an idempotent RPC (SET_PARAMETER,
// a diagnostic, a read) blindly re-dispatching is harmless. For these,
// it risks duplicating an object, repeating a factory reset or reboot,
// or retriggering a firmware flash the device may already be mid-way
// through (a brick risk, not just a redundant action) -- so these never
// auto-requeue on lease expiry, however many attempts remain; they
// dead-letter for explicit operator review instead. True reconciliation
// (inspecting device state before deciding) is future work; this is the
// safe default in the meantime -- never blind repetition of a
// destructive RPC.
var nonRepeatableTypes = []string{
	TypeAddObject, TypeDeleteObject, TypeReboot, TypeFactoryReset,
	TypeFirmwareDownload, TypeUpload,
}

// RecoveryResult reports what one RecoverExpiredLeases pass did.
type RecoveryResult struct {
	Requeued     int // lease expired, attempts remained, safe to retry -> back to QUEUED
	DeadLettered int // lease expired, attempts exhausted -> FAILED
	// DeadLetteredUnsafeRetry counts non-repeatable-type jobs dead-lettered
	// on lease expiry regardless of remaining attempts (audit P1.7).
	DeadLetteredUnsafeRetry int
	TimedOut                int // AWAITING_TRANSFER_COMPLETE past the transfer deadline -> FAILED
}

// RecoverExpiredLeases is the stale-lease reaper (audit P1.1). A job
// whose holder died mid-flight would otherwise sit in RPC_SENT /
// IN_PROGRESS forever, invisible to every Lease query. Non-repeatable
// types (audit P1.7 — see nonRepeatableTypes) always dead-letter on
// lease expiry, however many attempts remain; everything else goes back
// to QUEUED while attempts remain (Lease increments attempts on the
// next pickup, so a job that keeps stranding is bounded by max_attempts)
// and is dead-lettered as FAILED otherwise, with the reason recorded in
// fault_string. Legacy rows leased before the lease columns existed
// (leased_until IS NULL) are recovered on their started_at instead, so
// nothing already stranded stays stranded.
//
// The three passes below run in this order deliberately: the
// non-repeatable pass must run before the attempts-exhausted and
// requeue passes, since it needs to claim those jobs unconditionally
// (not just when attempts are exhausted) before the general passes
// would otherwise requeue them.
func (r *Repository) RecoverExpiredLeases(ctx context.Context) (RecoveryResult, error) {
	var res RecoveryResult

	unsafe, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'FAILED', completed_at = now(), updated_at = now(),
			fault_code = 'LEASE_EXPIRED_UNSAFE_RETRY',
			fault_string = 'job lease expired after ' || attempts || ' attempt(s); type ' || type ||
				' is not safe to blindly re-dispatch (it may already have executed on the device) -- requires manual review; last holder ' || COALESCE(lease_owner, 'unknown'),
			lease_owner = NULL, leased_until = NULL
		WHERE status IN ('RPC_SENT', 'IN_PROGRESS')
		  AND COALESCE(leased_until, started_at + $1 * interval '1 second') < now()
		  AND type = ANY($2)
	`, int(sessionLease.Seconds()), nonRepeatableTypes)
	if err != nil {
		return res, fmt.Errorf("dead-letter expired non-repeatable leases: %w", err)
	}
	if n, err := unsafe.RowsAffected(); err == nil {
		res.DeadLetteredUnsafeRetry = int(n)
	}

	dead, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'FAILED', completed_at = now(), updated_at = now(),
			fault_code = 'LEASE_EXPIRED',
			fault_string = 'job lease expired after ' || attempts || ' attempt(s); last holder ' || COALESCE(lease_owner, 'unknown'),
			lease_owner = NULL, leased_until = NULL
		WHERE status IN ('RPC_SENT', 'IN_PROGRESS')
		  AND COALESCE(leased_until, started_at + $1 * interval '1 second') < now()
		  AND attempts >= max_attempts
	`, int(sessionLease.Seconds()))
	if err != nil {
		return res, fmt.Errorf("dead-letter expired leases: %w", err)
	}
	if n, err := dead.RowsAffected(); err == nil {
		res.DeadLettered = int(n)
	}

	requeued, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'QUEUED', updated_at = now(), lease_owner = NULL, leased_until = NULL
		WHERE status IN ('RPC_SENT', 'IN_PROGRESS')
		  AND COALESCE(leased_until, started_at + $1 * interval '1 second') < now()
		  AND attempts < max_attempts
	`, int(sessionLease.Seconds()))
	if err != nil {
		return res, fmt.Errorf("requeue expired leases: %w", err)
	}
	if n, err := requeued.RowsAffected(); err == nil {
		res.Requeued = int(n)
	}

	timedOut, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'FAILED', completed_at = now(), updated_at = now(),
			fault_code = 'TRANSFER_TIMEOUT',
			fault_string = 'no TransferComplete received within ' || $1 || ' hours'
		WHERE status = 'AWAITING_TRANSFER_COMPLETE'
		  AND COALESCE(started_at, created_at) < now() - $1 * interval '1 hour'
	`, int(transferDeadline.Hours()))
	if err != nil {
		return res, fmt.Errorf("time out stale transfers: %w", err)
	}
	if n, err := timedOut.RowsAffected(); err == nil {
		res.TimedOut = int(n)
	}
	return res, nil
}

// CountStaleLeases counts leased jobs already past their deadline — the
// gauge that says the reaper is falling behind (or not running).
func (r *Repository) CountStaleLeases(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM jobs
		WHERE status IN ('RPC_SENT', 'IN_PROGRESS') AND leased_until < now()
	`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count stale leases: %w", err)
	}
	return n, nil
}
