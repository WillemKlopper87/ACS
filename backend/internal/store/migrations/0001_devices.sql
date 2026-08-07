-- Phase 1 (build plan §4 Phase 1 / design doc v3 §7.1, trimmed to the
-- columns Phase 1 actually populates). Columns for connection request,
-- credentials, software version, etc. are added by later-phase migrations
-- once the RPCs that populate them exist (GetParameterValues in Phase 2,
-- Connection Request in Phase 3, ...) — no point carrying dead nullable
-- columns ahead of the code that writes them.
CREATE TABLE devices (
    id UUID PRIMARY KEY,
    oui_serial TEXT UNIQUE NOT NULL,
    manufacturer TEXT,
    oui TEXT,
    product_class TEXT,
    serial_number TEXT,
    data_model_root TEXT NOT NULL DEFAULT 'UNKNOWN'
        CHECK (data_model_root IN ('DEVICE2', 'IGD1', 'UNKNOWN')),
    online_status TEXT NOT NULL DEFAULT 'OFFLINE'
        CHECK (online_status IN ('ONLINE', 'OFFLINE', 'UNREACHABLE')),
    last_inform_at TIMESTAMPTZ,
    last_inform_event_codes TEXT[],
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
