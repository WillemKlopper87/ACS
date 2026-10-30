package jobs

import (
	"context"
	"fmt"
)

// FaultSessionDropped marks a job whose RPC was sent in a session the CPE
// ended without answering, for a type that must not be blindly repeated.
const FaultSessionDropped = "SESSION_DROPPED_OUTCOME_UNKNOWN"

// DroppedSessionAction is what to do with a job left RPC_SENT by a CWMP
// session the CPE abandoned (observed on Nokia FastMile 1.2501.00.0798,
// which ends sessions mid-exchange without a fault).
type DroppedSessionAction int

const (
	// DroppedRequeue: idempotent read/write — send again in this session.
	DroppedRequeue DroppedSessionAction = iota
	// DroppedSucceeded: the new Inform proves the RPC executed (Reboot with
	// "M Reboot" carrying this job's CommandKey).
	DroppedSucceeded
	// DroppedAwaitTransfer: Download/Upload may have been accepted; wait for
	// TransferComplete (24 h deadline) instead of dead-lettering a transfer
	// that may be in progress or already flashed.
	DroppedAwaitTransfer
	// DroppedFailed: outcome unknown for a non-repeatable type; fail now for
	// operator review instead of after the 15-minute lease.
	DroppedFailed
)

// ClassifyDroppedSessionJob decides how to recover one job left RPC_SENT
// when its device sends a new Inform. A CPE runs one CWMP session at a time,
// so a new Inform means the earlier session is over and the RPC was never
// answered. methodKeys holds the CommandKeys carried by "M <method>" events
// of the new Inform, by method (e.g. "Reboot" -> {"reboot_2026...": true}).
func ClassifyDroppedSessionJob(job *Job, methodKeys map[string]map[string]bool) DroppedSessionAction {
	switch job.Type {
	case TypeReboot:
		if methodKeys["Reboot"][job.CommandKey] {
			return DroppedSucceeded
		}
		return DroppedFailed
	case TypeFirmwareDownload, TypeUpload:
		return DroppedAwaitTransfer
	case TypeAddObject, TypeDeleteObject, TypeFactoryReset:
		return DroppedFailed
	}
	if job.Attempts >= job.MaxAttempts {
		return DroppedFailed
	}
	return DroppedRequeue
}

// DroppedSessionJobs returns the device's session-dispatched jobs still
// RPC_SENT. Parameter discovery is excluded: it has its own object-level
// recovery (skip/defer the unanswered object).
func (r *Repository) DroppedSessionJobs(ctx context.Context, deviceID string) ([]*Job, error) {
	types := make([]string, 0, len(sessionDispatchableTypes))
	for _, t := range sessionDispatchableTypes {
		if t != TypeParameterDiscovery {
			types = append(types, t)
		}
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE device_id=$1 AND status=$2 AND type = ANY($3) ORDER BY created_at`,
		deviceID, StatusRPCSent, types)
	if err != nil {
		return nil, fmt.Errorf("load dropped-session jobs: %w", err)
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// ApplyDroppedSessionAction performs one recovery transition, guarded on the
// job still being the same RPC_SENT attempt so a concurrent response or
// another replica's recovery wins cleanly. It reports whether it applied.
func (r *Repository) ApplyDroppedSessionAction(ctx context.Context, job *Job, action DroppedSessionAction) (bool, error) {
	var query string
	args := []any{job.ID, job.Attempts}
	switch action {
	case DroppedRequeue:
		query = `UPDATE jobs SET status='QUEUED', updated_at=now(), lease_owner=NULL, leased_until=NULL WHERE id=$1 AND attempts=$2 AND status='RPC_SENT'`
	case DroppedSucceeded:
		query = `UPDATE jobs SET status='SUCCESS', completed_at=now(), updated_at=now(), lease_owner=NULL, leased_until=NULL WHERE id=$1 AND attempts=$2 AND status='RPC_SENT'`
	case DroppedAwaitTransfer:
		query = `UPDATE jobs SET status='AWAITING_TRANSFER_COMPLETE', updated_at=now(), lease_owner=NULL, leased_until=NULL WHERE id=$1 AND attempts=$2 AND status='RPC_SENT'`
	case DroppedFailed:
		query = `UPDATE jobs SET status='FAILED', completed_at=now(), updated_at=now(), lease_owner=NULL, leased_until=NULL,
			fault_code=$3, fault_string=$4 WHERE id=$1 AND attempts=$2 AND status='RPC_SENT'`
		args = append(args, FaultSessionDropped,
			"the CPE ended the CWMP session before answering this "+job.Type+" RPC; it may or may not have executed — review before retrying")
	default:
		return false, fmt.Errorf("unknown dropped-session action %d", action)
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("apply dropped-session recovery: %w", err)
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// LateTransferReconcilable reports whether a TransferComplete arriving for a
// job already marked FAILED should still be applied: the failure only
// recorded that the ACS lost track of the transfer (lease or session lost,
// or the 24 h deadline passed), not that the device reported a failure.
func LateTransferReconcilable(job *Job) bool {
	if job.Status != StatusFailed || job.FaultCode == nil {
		return false
	}
	switch *job.FaultCode {
	case "LEASE_EXPIRED_UNSAFE_RETRY", FaultSessionDropped, "TRANSFER_TIMEOUT":
		return job.Type == TypeFirmwareDownload || job.Type == TypeUpload
	}
	return false
}
