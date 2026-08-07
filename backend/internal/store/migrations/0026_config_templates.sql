-- Nice-to-have feature backlog: config templates — a named, reusable set
-- of parameter writes that can be bulk-applied to a device selection or
-- group on demand, or auto-applied on a device's first BOOTSTRAP Inform
-- (model_filter + auto_apply, the same model_filter concept
-- internal/policy already uses for continuous compliance, applied here to
-- one-shot initial provisioning).
CREATE TABLE config_templates (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT,
    parameters JSONB NOT NULL,        -- [{"name":..., "value":..., "type":...}, ...]
    model_filter TEXT,                -- matches manufacturer or product_class (ILIKE), NULL = never auto-applied
    auto_apply BOOLEAN NOT NULL DEFAULT false,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
