-- TR-069 diagnostics run after the CWMP session that triggered them ends
-- and announce completion with an Inform carrying "8 DIAGNOSTICS COMPLETE".
-- The ACS used to requeue the job straight back to QUEUED after the trigger
-- was acknowledged, so the same session leased it again and polled it to
-- TIMEOUT within seconds, before the CPE had even started the test.
-- AWAITING_DIAGNOSTICS_COMPLETE is invisible to Lease; the next Inform moves
-- the job back to QUEUED for exactly one poll per session, and
-- diagnostics_deadline bounds the whole wait by time instead of by attempts.
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_status_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_status_check
    CHECK (status IN ('QUEUED', 'RPC_SENT', 'IN_PROGRESS', 'AWAITING_TRANSFER_COMPLETE',
                      'AWAITING_DIAGNOSTICS_COMPLETE', 'SUCCESS', 'FAILED', 'TIMEOUT'));
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS diagnostics_deadline TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS jobs_awaiting_diagnostics_idx ON jobs (device_id)
    WHERE status = 'AWAITING_DIAGNOSTICS_COMPLETE';
