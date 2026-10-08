ALTER TABLE device_parameter_names ADD COLUMN IF NOT EXISTS evidence JSONB NOT NULL DEFAULT '{}';
CREATE TABLE device_discovery_snapshots (
 id BIGSERIAL PRIMARY KEY,
 device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
 command_key TEXT NOT NULL,
 protocol TEXT NOT NULL CHECK (protocol IN ('CWMP','USP')),
 root TEXT NOT NULL,
 names JSONB NOT NULL,
 capabilities JSONB NOT NULL DEFAULT '[]',
 evidence JSONB NOT NULL,
 discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(device_id, command_key)
);
CREATE INDEX device_discovery_snapshots_recent ON device_discovery_snapshots(device_id, discovered_at DESC);
