-- Per-device CPE->ACS Digest credentials (audit P0.5 remainder: "move to
-- unique per-device credentials"). A second credential_type joins the
-- existing CONNECTION_REQUEST rotation state machine: the ACS pushes
-- ManagementServer.Username/Password to one device, and cmd/acs's Digest
-- authenticator looks the presented username up here instead of (or as
-- well as) the fleet-wide ACS_DIGEST_* pair. Activation is automatic:
-- the first Inform authenticated with a PENDING credential proves the
-- CPE applied it. The partial index serves that per-Inform lookup.
ALTER TABLE device_credentials DROP CONSTRAINT device_credentials_credential_type_check;
ALTER TABLE device_credentials ADD CONSTRAINT device_credentials_credential_type_check
    CHECK (credential_type IN ('CONNECTION_REQUEST', 'CWMP_DIGEST'));
CREATE INDEX IF NOT EXISTS device_credentials_live_username_idx
    ON device_credentials (credential_type, username)
    WHERE status IN ('PENDING', 'ACTIVE', 'GRACE');
