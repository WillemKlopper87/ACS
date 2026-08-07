-- Basic append-only audit log (build plan §4 Phase 1 / design doc v3
-- §11.8). Phase 1 has no write RPCs yet (SetParameterValues etc. land in
-- Phase 2) and no operator REST actions yet, so the only events audited
-- here are CWMP session lifecycle events. The table shape already matches
-- v3 §11.8 so later phases add rows, not migrations.
CREATE TABLE audit_log (
    id UUID PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor TEXT NOT NULL,
    device_id UUID REFERENCES devices(id),
    action TEXT NOT NULL,
    details JSONB
);

CREATE INDEX audit_log_device_id_idx ON audit_log (device_id);
CREATE INDEX audit_log_occurred_at_idx ON audit_log (occurred_at);
