ALTER TABLE tmf_service_problems
    ADD COLUMN IF NOT EXISTS related_event_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS affected_resource_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS impact TEXT,
    ADD COLUMN IF NOT EXISTS severity TEXT,
    ADD COLUMN IF NOT EXISTS root_cause TEXT,
    ADD COLUMN IF NOT EXISTS resolution_date TIMESTAMPTZ;
