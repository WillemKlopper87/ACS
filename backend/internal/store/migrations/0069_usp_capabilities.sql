-- 0069: retain USP GetSupportedDM metadata in addition to the existing
-- parameter-name/writability cache used by write guards.
ALTER TABLE device_parameter_names
    ADD COLUMN capabilities JSONB NOT NULL DEFAULT '[]';
