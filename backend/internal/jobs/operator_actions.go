package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"acs/internal/parameters"
	"acs/internal/store"
)

// StatusCancelled: an operator withdrew the job before any RPC was sent.
const StatusCancelled = "CANCELLED"

var (
	// ErrNotCancellable: the job is no longer QUEUED. Once an RPC has gone
	// out the device may already have acted on it, so there is nothing
	// left to withdraw.
	ErrNotCancellable = errors.New("only a queued job can be cancelled")
	// ErrWorkflowOwned: the job belongs to a firmware rollout, a BSS order,
	// an onboarding, a credential rotation or the ACS itself. Those track
	// their jobs by outcome; cancelling or re-issuing one from outside
	// would leave them waiting on a job that never reports.
	ErrWorkflowOwned = errors.New("this job is managed by a rollout, a BSS order, onboarding or the system and cannot be changed here")
	// ErrNotRetryable: the job has not finished unsuccessfully, or is of a
	// type that is not safe to issue again unchanged.
	ErrNotRetryable = errors.New("this job cannot be retried")
	// ErrSecretsErased: the job carried a secret (a Wi-Fi passphrase, for
	// example) that was erased from its payload when it finished.
	ErrSecretsErased = errors.New("this job carried a secret that was erased when it finished; issue the action again")
)

// operatorManagedPredicate is true for a job an operator queued directly.
// It refers to the jobs row as "jobs".
const operatorManagedPredicate = `
	COALESCE(jobs.created_by, '') !~ '^(system(:|$)|service:|bss(:|-client:|$)|onboarding:|xmpp-|cgnat-)'
	AND NOT EXISTS (SELECT 1 FROM firmware_rollout_device rd WHERE rd.job_id = jobs.id)
	AND NOT EXISTS (SELECT 1 FROM bss_orders o WHERE o.command_key = jobs.command_key)
	AND NOT EXISTS (SELECT 1 FROM device_credentials c WHERE c.command_key = jobs.command_key)`

// retryableTypes can be issued again from the stored payload. Left out on
// purpose: firmware and file transfers (their URLs and tokens expire and a
// rollout tracks the original job), ADD_OBJECT (a retry after an unknown
// outcome can create a second object), FACTORY_RESET (must be asked for
// explicitly) and PARAMETER_DISCOVERY (it has its own de-duplication).
var retryableTypes = map[string]bool{
	TypeSetParameter: true, TypeGetParameter: true, TypeConnectionRequest: true,
	TypeReboot: true, TypeScheduleInform: true, TypeDeleteObject: true,
	TypeSetParameterAttributes: true, TypeGetParameterAttributes: true,
	TypeDiagnosticsPing: true, TypeDiagnosticsTraceroute: true,
}

// IsRetryableType reports whether Retry accepts jobs of this type.
func IsRetryableType(jobType string) bool { return retryableTypes[jobType] }

// IsFinishedUnsuccessfully reports whether a job ended without success.
func IsFinishedUnsuccessfully(status string) bool {
	return status == StatusFailed || status == StatusTimeout || status == StatusCancelled
}

func cancelNote(by string) string { return "cancelled by " + by }

// Cancel withdraws a QUEUED, operator-managed job. The status test and the
// update are one statement, so a job the gateway leases at the same moment
// is either cancelled before the lease sees it or refused here.
func (r *Repository) Cancel(ctx context.Context, id, by string) (*Job, error) {
	job, err := scanJob(r.db.QueryRowContext(ctx, `
		UPDATE jobs SET status = 'CANCELLED', completed_at = now(), updated_at = now(),
			fault_string = $2, lease_owner = NULL, leased_until = NULL
		WHERE id = $1 AND status = 'QUEUED' AND `+operatorManagedPredicate+`
		RETURNING `+jobColumns, id, cancelNote(by)))
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("cancel job: %w", err)
	}
	// Nothing was updated: say why.
	current, err := r.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status != StatusQueued {
		return nil, ErrNotCancellable
	}
	return nil, ErrWorkflowOwned
}

// CancelMany cancels every QUEUED, operator-managed job among ids and
// returns how many it cancelled. Jobs in any other state are left alone.
func (r *Repository) CancelMany(ctx context.Context, ids []string, by string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'CANCELLED', completed_at = now(), updated_at = now(),
			fault_string = $2, lease_owner = NULL, leased_until = NULL
		WHERE id::text = ANY($1) AND status = 'QUEUED' AND `+operatorManagedPredicate,
		store.StringArray(ids), cancelNote(by))
	if err != nil {
		return 0, fmt.Errorf("cancel jobs: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// operatorManaged reports whether a job was queued directly by an operator.
func (r *Repository) operatorManaged(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE jobs.id = $1 AND `+operatorManagedPredicate+`)`, id).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check job ownership: %w", err)
	}
	return ok, nil
}

// Retry queues a new job with the same device, type and payload as a job
// that ended FAILED, TIMEOUT or CANCELLED. The original is left as it is,
// for the record. The new job goes through the same protected-parameter
// check as any other.
func (r *Repository) Retry(ctx context.Context, id, by string) (*Job, error) {
	job, err := r.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !IsFinishedUnsuccessfully(job.Status) || !IsRetryableType(job.Type) {
		return nil, ErrNotRetryable
	}
	managed, err := r.operatorManaged(ctx, id)
	if err != nil {
		return nil, err
	}
	if !managed {
		return nil, ErrWorkflowOwned
	}
	if bytes.Contains(job.Payload, []byte(parameters.SecretRedacted)) {
		return nil, ErrSecretsErased
	}
	return r.CreateWithMaxAttempts(ctx, job.DeviceID, job.Type, job.Payload, by, job.MaxAttempts)
}
