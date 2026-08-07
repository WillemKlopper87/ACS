-- Phase 3 (build plan §4 Phase 3 / design doc v3 §12, §14 Phase 3).
--
-- Connection Request is ACS-initiated but, unlike SET_PARAMETER/
-- GET_PARAMETER, isn't triggered by an inbound CWMP request — nothing
-- about a device's own session dispatch queue applies to it. It gets its
-- own job status vocabulary additions:
--   IN_PROGRESS — the background worker (cmd/api) has leased the job and
--                 is either making the outbound HTTP GET or waiting to
--                 see if it provoked a new Inform.
--   TIMEOUT     — the GET succeeded (the CPE is reachable) but no Inform
--                 arrived within the wait window — the CGNAT case v3
--                 §12.4 specifically calls out, distinct from FAILED
--                 (the GET itself didn't succeed).
ALTER TABLE devices
    ADD COLUMN connection_request_url TEXT,
    ADD COLUMN connection_request_mode TEXT NOT NULL DEFAULT 'UNKNOWN'
        CHECK (connection_request_mode IN ('DIRECT_IPV4', 'DIRECT_IPV6', 'STUN_ANNEX_G', 'PERIODIC_FALLBACK_ONLY', 'UNKNOWN')),
    ADD COLUMN last_connection_request_at TIMESTAMPTZ,
    ADD COLUMN last_connection_request_status TEXT,
    ADD COLUMN last_inform_after_connection_request_at TIMESTAMPTZ;

ALTER TABLE jobs DROP CONSTRAINT jobs_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_type_check
    CHECK (type IN ('SET_PARAMETER', 'GET_PARAMETER', 'CONNECTION_REQUEST'));

ALTER TABLE jobs DROP CONSTRAINT jobs_status_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_status_check
    CHECK (status IN ('QUEUED', 'RPC_SENT', 'IN_PROGRESS', 'SUCCESS', 'FAILED', 'TIMEOUT'));
