-- Wi-Fi insights (docs/WIFI-MANAGEMENT-SPEC.md §3.3a): controller-style
-- bandwidth, client and SSID statistics. CPEs keep no history, so the ACS
-- stores cumulative counters per periodic sample and derives rates from
-- consecutive samples at query time (negative differences are counter
-- resets and are dropped). Client MACs and hostnames are personal data:
-- none of these tables is granted to grafana_ro.
CREATE TABLE IF NOT EXISTS wifi_samples (
    id BIGSERIAL PRIMARY KEY,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    sampled_at TIMESTAMPTZ NOT NULL,
    wireless_clients INTEGER NOT NULL,
    wired_clients INTEGER NOT NULL,
    guest_clients INTEGER NOT NULL,
    UNIQUE (device_id, sampled_at)
);
CREATE INDEX IF NOT EXISTS wifi_samples_sampled_at_idx ON wifi_samples (sampled_at);

CREATE TABLE IF NOT EXISTS wifi_ssid_samples (
    id BIGSERIAL PRIMARY KEY,
    sample_id BIGINT NOT NULL REFERENCES wifi_samples(id) ON DELETE CASCADE,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    sampled_at TIMESTAMPTZ NOT NULL,
    ssid_instance INTEGER NOT NULL,
    ssid_name TEXT,
    role TEXT,
    bytes_down BIGINT NOT NULL,
    bytes_up BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS wifi_ssid_samples_device_idx ON wifi_ssid_samples (device_id, ssid_instance, sampled_at);
CREATE INDEX IF NOT EXISTS wifi_ssid_samples_sample_idx ON wifi_ssid_samples (sample_id);

CREATE TABLE IF NOT EXISTS wifi_client_samples (
    id BIGSERIAL PRIMARY KEY,
    sample_id BIGINT NOT NULL REFERENCES wifi_samples(id) ON DELETE CASCADE,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    sampled_at TIMESTAMPTZ NOT NULL,
    mac TEXT NOT NULL,
    ssid_instance INTEGER,
    signal_dbm INTEGER,
    bytes_down BIGINT,
    bytes_up BIGINT
);
CREATE INDEX IF NOT EXISTS wifi_client_samples_device_idx ON wifi_client_samples (device_id, mac, sampled_at);
CREATE INDEX IF NOT EXISTS wifi_client_samples_sample_idx ON wifi_client_samples (sample_id);

-- One row per client per CPE. A gap longer than the session gap closes the
-- session; connected_seconds accumulates time between consecutive samples.
CREATE TABLE IF NOT EXISTS wifi_clients (
    id BIGSERIAL UNIQUE,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    mac TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    session_started_at TIMESTAMPTZ NOT NULL,
    sessions INTEGER NOT NULL DEFAULT 1,
    connected_seconds BIGINT NOT NULL DEFAULT 0,
    hostname TEXT,
    ip_address TEXT,
    ssid_instance INTEGER,
    ssid_name TEXT,
    band TEXT,
    mesh_node INTEGER,
    wireless BOOLEAN NOT NULL,
    guest BOOLEAN NOT NULL DEFAULT FALSE,
    private_mac BOOLEAN NOT NULL,
    vendor TEXT,
    signal_dbm INTEGER,
    PRIMARY KEY (device_id, mac)
);
CREATE INDEX IF NOT EXISTS wifi_clients_last_seen_idx ON wifi_clients (last_seen_at);
