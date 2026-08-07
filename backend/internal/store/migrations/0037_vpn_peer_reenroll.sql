-- Fixes a real bug caught by live-testing 0036: device_vpn_peers.device_id
-- was a blanket UNIQUE column, so revoking a peer left its REVOKED row
-- behind and any later re-enroll attempt hit the same unique constraint
-- and failed with a raw 23505 — "device already has a peer" forever,
-- even though EnrollDevice's own application-level check only meant to
-- block a second *ENROLLED* peer. A partial unique index expresses the
-- actual intent: at most one ENROLLED peer per device at a time, but
-- REVOKED history rows can accumulate freely.
ALTER TABLE device_vpn_peers DROP CONSTRAINT device_vpn_peers_device_id_key;

CREATE UNIQUE INDEX device_vpn_peers_device_enrolled_idx
    ON device_vpn_peers (device_id)
    WHERE status = 'ENROLLED';
