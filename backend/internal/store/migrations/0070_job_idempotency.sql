-- 0070: optional caller-supplied idempotency for durable ACS jobs.
ALTER TABLE jobs
    ADD COLUMN idempotency_key TEXT,
    ADD COLUMN request_hash TEXT;

CREATE UNIQUE INDEX jobs_device_type_idempotency_key_idx
    ON jobs (device_id, type, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
