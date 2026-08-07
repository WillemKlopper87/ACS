-- Phase 2 (build plan §4 Phase 2 / design doc v3 §7.4, trimmed to the
-- columns Phase 2 populates). lease_owner/lease_expires_at and the wider
-- job-state vocabulary (SCHEDULED, LEASED, IN_PROGRESS,
-- AWAITING_TRANSFER_COMPLETE, TIMEOUT, CANCELLED) from the full v3 design
-- are deferred to the phases that actually need them (crash recovery,
-- OTA, connection request) — see the same reasoning in 0001_devices.sql.
CREATE TABLE jobs (
    id UUID PRIMARY KEY,
    command_key TEXT UNIQUE NOT NULL,
    device_id UUID NOT NULL REFERENCES devices(id),
    type TEXT NOT NULL CHECK (type IN ('SET_PARAMETER', 'GET_PARAMETER')),
    status TEXT NOT NULL DEFAULT 'QUEUED'
        CHECK (status IN ('QUEUED', 'RPC_SENT', 'SUCCESS', 'FAILED')),
    payload JSONB NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    fault_code TEXT,
    fault_string TEXT
);

-- Serves both NextQueued's "oldest queued job for this device" lookup and
-- job-list-by-device queries the REST API will need later.
CREATE INDEX jobs_device_status_created_idx ON jobs (device_id, status, created_at);
