-- Reboot-aware liveness (field test 2026-10-30). After an ACS-requested
-- reboot a Nokia Beacon 3.1 needed 10.5 minutes to rejoin its mesh and
-- inform again; the fleet view showed ONLINE, then OFFLINE, with no hint
-- the outage was requested. REBOOTING covers the expected-down window.
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_online_status_check;
ALTER TABLE devices ADD CONSTRAINT devices_online_status_check
    CHECK (online_status IN ('ONLINE', 'OFFLINE', 'UNREACHABLE', 'REBOOTING'));
ALTER TABLE devices ADD COLUMN IF NOT EXISTS reboot_requested_at TIMESTAMPTZ;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_reboot_duration_seconds INTEGER;
