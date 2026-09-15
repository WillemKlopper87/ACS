-- Durable incident state. One active incident exists per tenant, device and
-- condition; the condition key is stable across repeated device faults.
CREATE TABLE alert_incidents (
    id UUID PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    condition_key TEXT NOT NULL,
    priority TEXT NOT NULL CHECK (priority IN ('P1','P2','P3','P4')),
    summary TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','acknowledged','suppressed','recovered','closed')),
    escalation_stage INTEGER NOT NULL DEFAULT 0 CHECK (escalation_stage >= 0),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    next_escalation_at TIMESTAMPTZ,
    acknowledged_at TIMESTAMPTZ,
    acknowledged_by TEXT,
    recovered_at TIMESTAMPTZ,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (tenant_id, device_id, condition_key)
);
CREATE INDEX alert_incidents_active_idx ON alert_incidents (state, next_escalation_at);
CREATE INDEX alert_incidents_tenant_idx ON alert_incidents (tenant_id, state, last_seen_at DESC);
