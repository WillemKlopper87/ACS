-- Build plan §4 Phase 5 firm-up: Traceroute, explicitly deferred from the
-- Phase 5 pass as "identical pattern [to Ping], not committed to this
-- session". Same trigger/poll shape via DiagnosticsState, different
-- TR-181 subtree (Device.IP.Diagnostics.TraceRoute.*).
ALTER TABLE jobs DROP CONSTRAINT jobs_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_type_check
    CHECK (type IN ('SET_PARAMETER', 'GET_PARAMETER', 'CONNECTION_REQUEST', 'FIRMWARE_DOWNLOAD', 'DIAGNOSTICS_PING', 'DIAGNOSTICS_TRACEROUTE'));
