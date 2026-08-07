-- Phase 4 deferred item, built in Phase 7 (build plan §4 Phase 4's own
-- note: "rollout tables... v3's own MVP puts this in Later" / design doc
-- v3 §9.5). firmware_rollout_device.state is NOT a free-standing status
-- column that could drift from the truth — DOWNLOADING/SUCCESS/FAILED
-- are read live from the jobs table (the single source of truth for job
-- outcome) via job_id; this table only tracks what's still ELIGIBLE
-- (not yet dispatched) versus dispatched, plus the eligibility computed
-- at rollout-creation time.
CREATE TABLE firmware_rollout (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    firmware_image_id UUID NOT NULL REFERENCES firmware_images(id),
    rollback_firmware_image_id UUID REFERENCES firmware_images(id),
    model_filter TEXT,             -- matches manufacturer or product_class, NULL = no filter
    current_version_filter TEXT,   -- matches cached Device.DeviceInfo.SoftwareVersion, NULL = no filter
    canary_percentage INTEGER NOT NULL DEFAULT 10 CHECK (canary_percentage BETWEEN 1 AND 100),
    maximum_failure_rate NUMERIC NOT NULL DEFAULT 0.2 CHECK (maximum_failure_rate BETWEEN 0 AND 1),
    maintenance_window_start_utc TIME,  -- NULL on both = no window restriction
    maintenance_window_end_utc TIME,
    status TEXT NOT NULL DEFAULT 'DRAFT'
        CHECK (status IN ('DRAFT', 'ACTIVE', 'BLOCKED', 'COMPLETED', 'ABORTED')),
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE firmware_rollout_device (
    rollout_id UUID NOT NULL REFERENCES firmware_rollout(id) ON DELETE CASCADE,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    job_id UUID REFERENCES jobs(id),  -- NULL until dispatched (state = ELIGIBLE)
    dispatched_at TIMESTAMPTZ,
    PRIMARY KEY (rollout_id, device_id)
);
