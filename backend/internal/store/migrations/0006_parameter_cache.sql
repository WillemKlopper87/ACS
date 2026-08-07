-- Phase 2 (design doc v3 §7.7). One JSONB blob per device, keyed by
-- parameter path, merged in place rather than replaced wholesale so a
-- partial GetParameterValues response doesn't erase unrelated cached
-- values (see parameters.Repository.Upsert).
CREATE TABLE device_parameter_cache (
    device_id UUID PRIMARY KEY REFERENCES devices(id),
    parameters JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
