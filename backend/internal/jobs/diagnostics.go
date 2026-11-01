package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// CWMP diagnostics lifecycle (TR-069 §A.4.1 / Annex N; field test on a
// Nokia FastMile behind CGNAT, 2026-10-30):
//
//	QUEUED --lease--> RPC_SENT (trigger: SetParameterValues ... DiagnosticsState=Requested)
//	  --SPV ack--> AWAITING_DIAGNOSTICS_COMPLETE (diagnostics_deadline set once)
//	  --next Inform ("8 DIAGNOSTICS COMPLETE" or any later session)--> QUEUED
//	  --lease--> RPC_SENT (poll: GetParameterValues)
//	  --state Requested/None--> AWAITING_DIAGNOSTICS_COMPLETE again (one poll per session)
//	  --state Complete / Error_*--> SUCCESS / FAILED
//	  --deadline passed--> TIMEOUT (on the poll, or by the reaper if the CPE never informs)
//
// The CPE only starts the test after the triggering session ends, so a poll
// in that same session can only ever read "Requested". Keeping the job out
// of Lease until a new Inform arrives is what stops the old behaviour of
// burning every attempt within one HTTP exchange.

const (
	// DiagnosticsMaxAttempts is a backstop only: one lease per CWMP session
	// means a 60 s inform interval polls about once a minute, so the time
	// deadline below ends a stuck diagnostic long before this is reached.
	DiagnosticsMaxAttempts = 40
	// diagnosticsBaseWindow is how long the ACS waits for a result beyond
	// the test's own expected run time: room for the CPE to start the test,
	// a dropped session or two and the next periodic Inform.
	diagnosticsBaseWindow = 10 * time.Minute
	// diagnosticsMaxWindow caps the computed window so a pathological input
	// (huge repetitions x timeout) cannot keep a job pending for days.
	diagnosticsMaxWindow = 2 * time.Hour
)

// IsCWMPDiagnosticType reports whether jobType uses the trigger/await/poll
// lifecycle above when dispatched over CWMP.
func IsCWMPDiagnosticType(jobType string) bool {
	switch jobType {
	case TypeDiagnosticsPing, TypeDiagnosticsTraceroute, TypeDiagnosticsDownload, TypeDiagnosticsUpload:
		return true
	}
	return false
}

// DiagnosticsTriggered reports whether the job's trigger has already been
// acknowledged by the CPE, i.e. the next dispatch must poll rather than
// (re)write DiagnosticsState=Requested. It depends on recorded state, not on
// the attempt counter, so a trigger lost to a dropped session is re-sent
// instead of being polled.
func DiagnosticsTriggered(job *Job) bool {
	return job != nil && job.DiagnosticsDeadline != nil
}

// DiagnosticsWindow is how long after the trigger is acknowledged the ACS
// keeps waiting for a result: the base window plus the worst-case run time
// implied by the job's own inputs (milliseconds in TR-181).
func DiagnosticsWindow(job *Job) time.Duration {
	var expected time.Duration
	if job != nil {
		switch job.Type {
		case TypeDiagnosticsPing:
			var p DiagnosticsPingPayload
			if json.Unmarshal(job.Payload, &p) == nil && p.NumberOfRepetitions > 0 && p.Timeout > 0 {
				expected = time.Duration(p.NumberOfRepetitions) * time.Duration(p.Timeout) * time.Millisecond
			}
		case TypeDiagnosticsTraceroute:
			var p DiagnosticsTraceroutePayload
			if json.Unmarshal(job.Payload, &p) == nil && p.NumberOfTries > 0 && p.Timeout > 0 && p.MaxHopCount > 0 {
				expected = time.Duration(p.NumberOfTries) * time.Duration(p.Timeout) * time.Duration(p.MaxHopCount) * time.Millisecond
			}
		}
	}
	window := diagnosticsBaseWindow + expected
	if window > diagnosticsMaxWindow || window < 0 {
		window = diagnosticsMaxWindow
	}
	return window
}

// DiagnosticsExpired reports whether a triggered diagnostic is past its
// deadline at now.
func DiagnosticsExpired(job *Job, now time.Time) bool {
	return job != nil && job.DiagnosticsDeadline != nil && now.After(*job.DiagnosticsDeadline)
}

// MarkAwaitingDiagnostics parks a diagnostic whose trigger (or a poll that
// still read "Requested") was just answered, so no further RPC for it is
// sent in the current session. The deadline is set on the first transition
// only and kept afterwards. Guarded on the job still being this RPC_SENT
// attempt; reports whether it applied.
func (r *Repository) MarkAwaitingDiagnostics(ctx context.Context, job *Job, window time.Duration) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'AWAITING_DIAGNOSTICS_COMPLETE', updated_at = now(), lease_owner = NULL, leased_until = NULL,
			diagnostics_deadline = COALESCE(diagnostics_deadline, now() + $3 * interval '1 second')
		WHERE id = $1 AND attempts = $2 AND status = 'RPC_SENT'
	`, job.ID, job.Attempts, int(window.Seconds()))
	if err != nil {
		return false, fmt.Errorf("mark job awaiting diagnostics: %w", err)
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// ReleaseAwaitingDiagnostics returns every diagnostic of deviceID parked in
// AWAITING_DIAGNOSTICS_COMPLETE to QUEUED. Called once per Inform, i.e. once
// per CWMP session, which is what limits polling to one GetParameterValues
// per diagnostic per session. Returns how many jobs were released.
func (r *Repository) ReleaseAwaitingDiagnostics(ctx context.Context, deviceID string) (int, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'QUEUED', updated_at = now()
		WHERE device_id = $1 AND status = 'AWAITING_DIAGNOSTICS_COMPLETE'
	`, deviceID)
	if err != nil {
		return 0, fmt.Errorf("release awaiting diagnostics: %w", err)
	}
	n, err := result.RowsAffected()
	return int(n), err
}

// timeOutAwaitingDiagnostics is the reaper pass for diagnostics whose CPE
// never came back (no Inform at all) before the deadline.
func (r *Repository) timeOutAwaitingDiagnostics(ctx context.Context) (int, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'TIMEOUT', completed_at = now(), updated_at = now(),
			fault_code = 'DIAGNOSTICS_TIMEOUT',
			fault_string = 'diagnostic result not available by its deadline (no Inform with 8 DIAGNOSTICS COMPLETE and no poll reading a final DiagnosticsState)'
		WHERE status = 'AWAITING_DIAGNOSTICS_COMPLETE' AND diagnostics_deadline < now()
	`)
	if err != nil {
		return 0, fmt.Errorf("time out awaiting diagnostics: %w", err)
	}
	n, err := result.RowsAffected()
	return int(n), err
}
