-- Phase 7 (build plan §4 Phase 7 / design doc v3 Phase 7: "Policy
-- engine"). v3 gives no further detail than that bullet — this is a
-- deliberate, concrete scope for it: continuous compliance enforcement,
-- not a rules DSL. A policy says "devices matching this filter should
-- report this parameter as this value"; cmd/acs checks every Inform's
-- reported parameters against active policies and queues a correcting
-- SET_PARAMETER the moment a match drifts, rather than waiting for an
-- operator (or a Phase 7 scheduled job) to notice. Distinct from both
-- scheduled_jobs (time-triggered) and firmware_rollout (one-time push):
-- this is event-triggered, on every check-in, indefinitely.
CREATE TABLE policies (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    model_filter TEXT,  -- matches manufacturer or product_class, NULL = applies fleet-wide
    parameter_name TEXT NOT NULL,
    desired_value TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
