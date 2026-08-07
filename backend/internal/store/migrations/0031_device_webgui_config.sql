-- Admin-platform backlog: device web-GUI embed, same "scaffold now,
-- functional later" call as SSH/Telnet (0030) and the same CGNAT
-- reachability constraint. One config per device (not versioned/rotated
-- like device_cli_credentials — a device's own web UI base URL and
-- optional HTTP Basic Auth pair change rarely, so a single row that gets
-- overwritten is simpler and matches this feature's much lower churn).
CREATE TABLE device_webgui_config (
    device_id UUID PRIMARY KEY REFERENCES devices(id),
    base_url TEXT NOT NULL,
    username TEXT,
    password TEXT, -- encrypted at rest when ACS_CREDENTIAL_ENCRYPTION_KEY is set, same enc: prefix convention as device_cli_credentials
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
