-- Phase 1 session tracking (build plan §4 Phase 1 / design doc v3 §7.3,
-- trimmed: no timeout_at/last_activity_at yet — those back the full
-- session timer state machine, which lands with serial RPC dispatch in
-- Phase 2 (v3 §5.2/§5.4).
CREATE TABLE cwmp_sessions (
    id UUID PRIMARY KEY,
    device_id UUID NOT NULL REFERENCES devices(id),
    state TEXT NOT NULL,
    inform_event_codes TEXT[],
    opened_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at TIMESTAMPTZ,
    close_reason TEXT
);

CREATE INDEX cwmp_sessions_device_id_idx ON cwmp_sessions (device_id);
