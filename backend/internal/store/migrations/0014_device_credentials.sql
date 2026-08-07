-- Phase 6 (build plan §4 Phase 6 / design doc v3 §11.6: "ACS-to-CPE
-- Connection Request credential rotation"). Versioned per-device
-- credentials, not an in-place update — v3's flow explicitly needs a
-- grace period (old credential still valid while the new one is
-- confirmed) and an audit trail of what changed when, neither of which
-- survive an UPDATE that overwrites the old value.
--
-- credential_type is a single value today (CONNECTION_REQUEST) but kept
-- as a column, not assumed, since v3 §11.3 lists five separate credential
-- classes — this table only owns one of them.
CREATE TABLE device_credentials (
    id UUID PRIMARY KEY,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    credential_type TEXT NOT NULL CHECK (credential_type IN ('CONNECTION_REQUEST')),
    version INTEGER NOT NULL,
    username TEXT NOT NULL,
    password TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN ('PENDING', 'ACTIVE', 'GRACE', 'REVOKED')),
    command_key TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    UNIQUE (device_id, credential_type, version)
);

-- At most one ACTIVE credential per device+type at a time — Activate()
-- demotes the previous ACTIVE row to GRACE in the same transaction, so
-- this constraint should never actually be tested in practice, but it's
-- the real invariant "switch the client to the new credential" depends
-- on: exactly one unambiguous answer to "what's active right now."
CREATE UNIQUE INDEX device_credentials_one_active_idx
    ON device_credentials (device_id, credential_type)
    WHERE status = 'ACTIVE';
