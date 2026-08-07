-- Admin-platform backlog, deliberately last: the WireGuard tunnel
-- concentrator. This table is the peer registry (per-device keypair +
-- overlay IP) — one row per enrolled device, same "scaffold now,
-- functional later" posture as SSH/Telnet (0030) and the device web-GUI
-- embed (0031), and for the same underlying reason: TR-069 has no native
-- "here's your VPN config" RPC, and most consumer/rebranded CPE firmware
-- (the ZOWEE test unit included) can't enroll in a VPN you control at
-- all. What's real here: keypair generation (Curve25519, unambiguous —
-- unlike Annex G's undocumented signature format) and overlay IP
-- allocation. What this table does NOT do on its own: stand up an actual
-- OS-level WireGuard interface — that needs a separate concentrator
-- process (wireguard-go or the kernel module) applying this table via
-- `wg syncconf`, which is out of scope for admin-panel code and wasn't
-- attempted here.
CREATE TABLE device_vpn_peers (
    id UUID PRIMARY KEY,
    device_id UUID NOT NULL UNIQUE REFERENCES devices(id),
    public_key TEXT NOT NULL,
    private_key TEXT NOT NULL, -- encrypted at rest when ACS_CREDENTIAL_ENCRYPTION_KEY is set, same enc: prefix convention as device_cli_credentials/device_webgui_config
    overlay_ip TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'ENROLLED' CHECK (status IN ('ENROLLED', 'REVOKED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);

CREATE INDEX device_vpn_peers_status_idx ON device_vpn_peers (status);
