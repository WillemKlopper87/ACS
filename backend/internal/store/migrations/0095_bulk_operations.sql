-- Bulk operations and operator job control (docs/DEVICE-MANAGEMENT-PLAN.md).
--
-- 1. A job an operator queued can be cancelled while it is still QUEUED.
--    CANCELLED is a finished state: nothing leases it, the retention job
--    prunes it, and its payload secrets are erased like any finished job's.
-- 2. A fan-out (a bulk action or a template apply) is recorded as one
--    bulk operation with one item per requested device, so its progress
--    can be read back after the request that started it has returned.

ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_status_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_status_check
    CHECK (status IN ('QUEUED', 'RPC_SENT', 'IN_PROGRESS', 'AWAITING_TRANSFER_COMPLETE',
                      'AWAITING_DIAGNOSTICS_COMPLETE', 'SUCCESS', 'FAILED', 'TIMEOUT', 'CANCELLED'));

-- Same trigger as 0086, with CANCELLED added to the finished states.
DROP TRIGGER jobs_redact_finished_secrets ON jobs;
CREATE TRIGGER jobs_redact_finished_secrets
    BEFORE UPDATE OF status ON jobs
    FOR EACH ROW
    WHEN (NEW.status IN ('SUCCESS', 'FAILED', 'TIMEOUT', 'CANCELLED') AND OLD.status IS DISTINCT FROM NEW.status)
    EXECUTE FUNCTION redact_finished_job_secrets();

CREATE TABLE bulk_operations (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind       TEXT NOT NULL CHECK (kind IN ('BULK_ACTION', 'TEMPLATE_APPLY')),
    action     TEXT NOT NULL,
    -- What was asked, in words. Parameter names only, never values.
    label      TEXT NOT NULL,
    group_id   UUID REFERENCES device_groups(id) ON DELETE SET NULL,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX bulk_operations_created_idx ON bulk_operations (created_at DESC);

-- One row per requested device, in request order. job_id follows the
-- latest job for that device: a retry replaces it. A device that could
-- not be queued has no job and carries the reason in error.
CREATE TABLE bulk_operation_items (
    bulk_operation_id   UUID NOT NULL REFERENCES bulk_operations(id) ON DELETE CASCADE,
    position            INT  NOT NULL,
    requested_device_id TEXT NOT NULL,
    device_id           UUID REFERENCES devices(id) ON DELETE SET NULL,
    job_id              UUID REFERENCES jobs(id) ON DELETE SET NULL,
    command_key         TEXT,
    error               TEXT,
    detail              TEXT,
    retries             INT NOT NULL DEFAULT 0,
    PRIMARY KEY (bulk_operation_id, position)
);
CREATE INDEX bulk_operation_items_job_idx ON bulk_operation_items (job_id);
CREATE INDEX bulk_operation_items_device_idx ON bulk_operation_items (device_id);
