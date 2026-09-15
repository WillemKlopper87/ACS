-- Persist the ordered escalation steps configured for each alert policy.
ALTER TABLE alert_policies
    ADD COLUMN steps JSONB NOT NULL DEFAULT '[]'::jsonb;
