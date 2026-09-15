-- Configurable CPE escalation policies. Scope precedence is enforced by the
-- alerting resolver: device, group, tenant, then fleet.
CREATE TABLE alert_policies (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    scope TEXT NOT NULL CHECK (scope IN ('fleet','tenant','group','device')),
    tenant_id TEXT,
    group_id UUID REFERENCES device_groups(id) ON DELETE CASCADE,
    device_id UUID REFERENCES devices(id) ON DELETE CASCADE,
    customer_tier TEXT,
    enabled BOOLEAN NOT NULL DEFAULT true,
    fault_priorities JSONB NOT NULL DEFAULT '{}'::jsonb,
    offline_after_seconds INTEGER NOT NULL DEFAULT 900 CHECK (offline_after_seconds >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((scope = 'fleet' AND tenant_id IS NULL AND group_id IS NULL AND device_id IS NULL) OR
           (scope = 'tenant' AND tenant_id IS NOT NULL AND group_id IS NULL AND device_id IS NULL) OR
           (scope = 'group' AND group_id IS NOT NULL AND device_id IS NULL) OR
           (scope = 'device' AND device_id IS NOT NULL))
);
CREATE INDEX alert_policies_scope_lookup_idx ON alert_policies (scope, tenant_id, group_id, device_id, enabled);
