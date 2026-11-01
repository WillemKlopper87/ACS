-- Sealed secret handles (I4). A BSS stores a secret (a Wi-Fi passphrase)
-- with POST /bss/v1/secrets and names the returned handle in an order, so
-- the value never sits in bss_orders, a job payload or an audit record.
-- sealed is AES-256-GCM under ACS_BSS_SECRET_KEY (not the database
-- credentials), with the handle and owner as additional data. An order
-- claims the handle for one device (device_id); the CWMP gateway deletes
-- the row when it sends the write, and expired rows are swept.
CREATE TABLE bss_secrets (
    id          TEXT PRIMARY KEY,
    customer_id UUID REFERENCES customers(id) ON DELETE CASCADE,
    sealed      BYTEA NOT NULL,
    device_id   TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at  TIMESTAMPTZ,
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX bss_secrets_expires_idx ON bss_secrets (expires_at);
