-- Admin-platform backlog: SSH/Telnet console access, scaffolded per the
-- user's explicit "build now, functional later" call (2026-08-06) — the
-- ACS reaching a device's shell port has the identical CGNAT reachability
-- constraint as Connection Request/Annex G (see 0029), just on port 22/23
-- instead of 7547, so this is unusable against a NAT'd device until a
-- VPN/tunnel path exists. Credentials are per-device, not shared, since
-- unlike the CWMP Connection Request password (one shared secret works
-- fleet-wide because the ACS chose it), SSH/Telnet credentials are
-- whatever the device's own OS-level account already is.
CREATE TABLE device_cli_credentials (
    id UUID PRIMARY KEY,
    device_id UUID NOT NULL REFERENCES devices(id),
    protocol TEXT NOT NULL CHECK (protocol IN ('SSH', 'TELNET')),
    host TEXT NOT NULL,
    port INTEGER NOT NULL,
    username TEXT NOT NULL,
    password TEXT NOT NULL, -- encrypted at rest when ACS_CREDENTIAL_ENCRYPTION_KEY is set, same enc: prefix convention as internal/credentials
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX device_cli_credentials_device_id_idx ON device_cli_credentials(device_id);
