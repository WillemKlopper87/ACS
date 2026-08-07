-- Build plan §4 Phase 4 firm-up: automatic rollback dispatch when a
-- rollout gets BLOCKED (failure rate breach) and a rollback image was
-- configured at creation time. rollback_dispatched_at makes that dispatch
-- idempotent — a rollout can only be blocked once in its lifetime in the
-- current state machine, but this guards against a retried request
-- double-queuing rollback downloads.
ALTER TABLE firmware_rollout ADD COLUMN rollback_dispatched_at TIMESTAMPTZ;
