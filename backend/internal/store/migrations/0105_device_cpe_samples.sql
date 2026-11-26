-- Cellular CPE signal history: one row per recorded reading of a cellular
-- CPE's radio metrics (RSRP/RSRQ/SINR/RSSI in dB or dBm, the serving band
-- and the radio access technology), taken at its check-ins and charted on
-- the device page. Only radio measurements: no subscriber identifier, no
-- cell id and no location are stored. Pruned by internal/retention.
CREATE TABLE IF NOT EXISTS device_cpe_samples (
    id BIGSERIAL PRIMARY KEY,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    sampled_at TIMESTAMPTZ NOT NULL,
    rsrp DOUBLE PRECISION NULL,
    rsrq DOUBLE PRECISION NULL,
    snr DOUBLE PRECISION NULL,
    rssi DOUBLE PRECISION NULL,
    band TEXT NOT NULL DEFAULT '' CHECK (char_length(band) <= 64),
    rat TEXT NOT NULL DEFAULT '' CHECK (char_length(rat) <= 64),
    UNIQUE (device_id, sampled_at)
);
CREATE INDEX IF NOT EXISTS device_cpe_samples_sampled_at_idx ON device_cpe_samples (sampled_at);
