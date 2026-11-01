-- TR-069 Annex K XMPP Connection Request (field test 2026-10-30). A Nokia
-- FastMile 5G Gateway 2 behind carrier CGNAT advertises a private
-- ConnectionRequestURL and has no STUN, so HTTP and Annex G Connection
-- Request can never reach it — but it supports XMPP ("XMPP,HTTP" in
-- SupportedConnReqMethods) with a writable Device.XMPP.Connection. table.
--
-- 1. XMPP joins the reachability modes: set once an XMPP Connection Request
--    has provoked a "6 CONNECTION REQUEST" Inform.
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_connection_request_mode_check;
ALTER TABLE devices ADD CONSTRAINT devices_connection_request_mode_check
    CHECK (connection_request_mode IN ('DIRECT_IPV4', 'DIRECT_IPV6', 'STUN_ANNEX_G', 'XMPP', 'PERIODIC_FALLBACK_ONLY', 'UNKNOWN'));

-- 2. The Inform-time NAT judgement (ConnectionRequestURL host vs. Inform
--    source) used to live only in connection_request_mode
--    (PERIODIC_FALLBACK_ONLY). Once a NAT'd device is reached over XMPP its
--    mode becomes XMPP, so the judgement is kept separately: if the ACS's
--    XMPP session is down, the worker still skips the pointless HTTP dial.
--    NULL = never judged.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS connection_request_behind_nat BOOLEAN;

-- 3. Per-device XMPP account credentials (the CPE's login to the ACS's XMPP
--    server, Device.XMPP.Connection.{i}.Username/Password). Same versioned,
--    encrypted-at-rest lifecycle as the other two types; it becomes ACTIVE
--    once the CPE reports a ConnReqJabberID, i.e. it has logged in with it.
ALTER TABLE device_credentials DROP CONSTRAINT device_credentials_credential_type_check;
ALTER TABLE device_credentials ADD CONSTRAINT device_credentials_credential_type_check
    CHECK (credential_type IN ('CONNECTION_REQUEST', 'CWMP_DIGEST', 'XMPP'));

-- 4. Zero-touch XMPP provisioning is a multi-session conversation with the
--    CPE (AddObject returns the instance number, only a later
--    GetParameterNames proves which leaves exist and are writable), so it is
--    a persisted, resumable state machine rather than one blind batch. One
--    row per device; job_id is the job the current state waits on. No
--    secrets are stored here — the password lives in device_credentials.
CREATE TABLE device_xmpp_provisioning (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK (state IN (
        'ADD_CONNECTION', 'DISCOVER_CONNECTION', 'ADD_SERVER', 'DISCOVER_SERVER',
        'CONFIGURE', 'BIND', 'VERIFY', 'DONE', 'FAILED')),
    credential_id UUID REFERENCES device_credentials(id) ON DELETE SET NULL,
    connection_path TEXT,
    server_path TEXT,
    job_id UUID,
    attempts INTEGER NOT NULL DEFAULT 0,
    domain TEXT NOT NULL,
    resource TEXT NOT NULL,
    server_address TEXT NOT NULL,
    server_port INTEGER NOT NULL CHECK (server_port BETWEEN 1 AND 65535),
    keepalive_interval INTEGER NOT NULL,
    acs_jid TEXT NOT NULL,
    relax_cert_verification BOOLEAN NOT NULL DEFAULT false,
    bypass_hostname_check BOOLEAN NOT NULL DEFAULT false,
    jabber_id TEXT,
    connection_status TEXT,
    last_error TEXT,
    requested_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX device_xmpp_provisioning_active_idx
    ON device_xmpp_provisioning (updated_at)
    WHERE state NOT IN ('DONE', 'FAILED');
