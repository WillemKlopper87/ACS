-- Phase 7 (build plan §4 Phase 7 / design doc v3 Phase 7: "Scheduled
-- jobs"). Fixed-interval recurring dispatch, not a full cron expression
-- engine — a lab-scope simplification, documented as deliberate: an
-- operator's real needs here ("refresh WiFi client stats every hour")
-- are all expressible as an interval, and a cron parser is a dependency
-- this doesn't need yet.
CREATE TABLE scheduled_jobs (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    job_type TEXT NOT NULL
        CHECK (job_type IN ('SET_PARAMETER', 'GET_PARAMETER', 'DIAGNOSTICS_PING', 'CONNECTION_REQUEST')),
    target_type TEXT NOT NULL CHECK (target_type IN ('DEVICE', 'GROUP')),
    target_id UUID NOT NULL,
    payload JSONB NOT NULL,
    interval_seconds INTEGER NOT NULL CHECK (interval_seconds >= 60),
    enabled BOOLEAN NOT NULL DEFAULT true,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_run_at TIMESTAMPTZ,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX scheduled_jobs_due_idx ON scheduled_jobs (next_run_at) WHERE enabled;
