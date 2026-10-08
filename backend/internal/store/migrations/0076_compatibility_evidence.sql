CREATE TABLE device_instance_evidence (
 device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
 requested_path TEXT NOT NULL,
 firmware TEXT NOT NULL DEFAULT '',
 instances JSONB NOT NULL,
 discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(device_id,requested_path)
);
CREATE TABLE device_operation_evidence (
 device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
 command_key TEXT NOT NULL,
 firmware TEXT NOT NULL DEFAULT '',
 operation TEXT NOT NULL CHECK(operation IN ('READ','WRITE')),
 success BOOLEAN NOT NULL,
 paths JSONB NOT NULL,
 detail JSONB NOT NULL DEFAULT '{}',
 tested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(device_id,command_key)
);
CREATE INDEX device_operation_evidence_recent ON device_operation_evidence(device_id,tested_at DESC);
