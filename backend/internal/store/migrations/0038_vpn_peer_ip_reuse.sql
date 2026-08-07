-- Same class of bug as 0037, caught by the same live re-enroll test:
-- overlay_ip was also a blanket UNIQUE column, so a REVOKED peer's
-- address stayed permanently claimed even though AllocateOverlayIP (and
-- EnrollDevice's own "used" query) correctly only counts ENROLLED peers
-- as occupying an address — the allocator picks the freed IP, then the
-- INSERT hits this constraint. Same partial-unique-index fix.
ALTER TABLE device_vpn_peers DROP CONSTRAINT device_vpn_peers_overlay_ip_key;

CREATE UNIQUE INDEX device_vpn_peers_overlay_ip_enrolled_idx
    ON device_vpn_peers (overlay_ip)
    WHERE status = 'ENROLLED';
