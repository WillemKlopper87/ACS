-- Preserve the CWMP namespace negotiated by the CPE for the lifetime of
-- the HTTP/CWMP session. ACS-initiated RPCs must use the same namespace
-- version as the Inform that opened the session; default 1-0 keeps old
-- rows/backward compatibility intact.
ALTER TABLE cwmp_sessions
    ADD COLUMN IF NOT EXISTS cwmp_namespace TEXT NOT NULL
    DEFAULT 'urn:dslforum-org:cwmp-1-0';
