-- OAuth2 client-credentials auth for cmd/bssadapter (replaces the
-- single-shared-token interim mechanism the integration guide always
-- flagged as temporary — see bss-integration-guide.md §3). Each row is
-- one registered BSS/CRM integration; client_secret is bcrypt-hashed,
-- never stored or returned in plaintext after creation, same posture as
-- operators.password_hash.
CREATE TABLE bss_oauth_clients (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    client_id TEXT NOT NULL UNIQUE,
    client_secret_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);
