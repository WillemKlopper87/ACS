-- 0068: persist model-aware device-profile assignment provenance.
-- Assignment does not authorize writes; callers still validate discovery and policy.
ALTER TABLE devices
    ADD COLUMN profile_id TEXT,
    ADD COLUMN profile_matched_by TEXT,
    ADD COLUMN profile_qualified BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN profile_evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN profile_assigned_at TIMESTAMPTZ;

ALTER TABLE devices
    ADD CONSTRAINT devices_profile_matched_by_check
    CHECK (profile_matched_by IS NULL OR profile_matched_by IN ('model', 'vendor', 'oui', 'fallback'));

CREATE INDEX devices_profile_id_idx ON devices (profile_id);
