-- Nice-to-have backlog: parameter discovery. A GetParameterNames(root, false)
-- sweep run automatically on a device's first connect (BOOTSTRAP) and
-- available on demand thereafter, so the console can show what a CPE
-- actually supports instead of relying solely on a static vendor registry —
-- and so the ACS learns the device's real data model root instead of
-- leaving it UNKNOWN forever (see devices.DataModelRoot, never written
-- before this migration).
--
-- One JSONB blob per device (name -> writable bool), replaced wholesale on
-- each discovery run rather than merged — unlike the parameter *value*
-- cache (0006_parameter_cache.sql), a stale entry here (a parameter the
-- firmware no longer exposes) is actively misleading, so a fresh discovery
-- should fully supersede the last one.
CREATE TABLE device_parameter_names (
    device_id UUID PRIMARY KEY REFERENCES devices(id),
    names JSONB NOT NULL DEFAULT '{}',
    discovered_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE devices ADD COLUMN data_model_root_confirmed_at TIMESTAMPTZ;
