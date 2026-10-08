CREATE TABLE bulk_data_receivers (
 id UUID PRIMARY KEY,
 device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
 profile_instance INTEGER NOT NULL CHECK(profile_instance > 0),
 parameter_paths JSONB NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 revoked BOOLEAN NOT NULL DEFAULT false,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX bulk_data_receivers_device ON bulk_data_receivers(device_id);
CREATE TABLE bulk_data_reports (
 id BIGSERIAL PRIMARY KEY,
 receiver_id UUID NOT NULL REFERENCES bulk_data_receivers(id) ON DELETE CASCADE,
 sha256 TEXT NOT NULL,
 report JSONB NOT NULL,
 received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(receiver_id,sha256)
);
CREATE INDEX bulk_data_reports_recent ON bulk_data_reports(receiver_id,received_at DESC);
