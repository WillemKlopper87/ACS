-- On-demand session capture (design docs/superpowers/specs/
-- 2026-09-14-session-capture-design.md). match_value is always
-- expressed in its mode's own terms: 'device' and 'identity' both use
-- the device's oui_serial natural key (cwmp.DeviceID.NaturalKey()
-- shape), deliberately the same representation for both so cmd/acs's
-- per-event check is one query shape; 'remote_ip' uses a single exact
-- IP address string, never a CIDR range. device_id is a denormalized
-- resolution column, never the match key itself even for
-- match_type='device'.
CREATE TABLE capture_sessions (
    id           UUID PRIMARY KEY,
    device_id    UUID REFERENCES devices(id),
    match_type   TEXT NOT NULL CHECK (match_type IN ('device','identity','remote_ip')),
    match_value  TEXT NOT NULL,
    protocol     TEXT NOT NULL CHECK (protocol IN ('CWMP','USP')),
    status       TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','STOPPED','EXPIRED')),
    started_by   TEXT NOT NULL,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    stopped_at   TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX capture_sessions_active_match_idx
    ON capture_sessions (match_type, match_value) WHERE status = 'ACTIVE';
CREATE INDEX capture_sessions_device_idx ON capture_sessions (device_id) WHERE device_id IS NOT NULL;

CREATE TABLE capture_events (
    id          UUID PRIMARY KEY,
    session_id  UUID NOT NULL REFERENCES capture_sessions(id) ON DELETE CASCADE,
    seq         INTEGER NOT NULL,
    direction   TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
    kind        TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    summary     TEXT NOT NULL,
    body        TEXT,
    UNIQUE (session_id, seq)
);
CREATE INDEX capture_events_session_idx ON capture_events (session_id, seq);
