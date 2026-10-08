ALTER TABLE bulk_data_receivers ADD COLUMN reporting_interval INTEGER NOT NULL DEFAULT 300 CHECK(reporting_interval BETWEEN 60 AND 86400);
ALTER TABLE bulk_data_receivers ADD COLUMN last_report_at TIMESTAMPTZ;
ALTER TABLE bulk_data_receivers ADD COLUMN renewed_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX bulk_data_receivers_profile_recent ON bulk_data_receivers(device_id,profile_instance,created_at DESC);
