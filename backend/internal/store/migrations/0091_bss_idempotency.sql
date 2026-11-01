-- Idempotency-Key on /bss/v1 writes (POST /bss/v1/mappings, including
-- pre_provision). A key is scoped to the BSS account the request is for,
-- which the caller was authorized for, so one tenant's key never answers
-- another's request. status is NULL while the first request is in flight.
-- request_hash is a SHA-256 of the canonical request (it may carry SIM
-- identifiers, so the request itself is not stored); response is the body
-- returned, which never does.
CREATE TABLE bss_idempotency_keys (
    account_id   TEXT NOT NULL,
    key          TEXT NOT NULL,
    operation    TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status       INT,
    response     BYTEA,
    location     TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (account_id, key)
);

CREATE INDEX bss_idempotency_keys_created_idx ON bss_idempotency_keys (created_at);
