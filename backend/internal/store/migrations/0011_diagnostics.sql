-- Phase 5 (build plan §4 Phase 5 / design doc v3 §10.1). No new job
-- status needed here, unlike Phase 3/4 — TR-069 diagnostics have a
-- different shape entirely: not an RPC-triggered request/response, but
-- ACS writes input parameters (Host, NumberOfRepetitions,
-- DiagnosticsState=Requested) via ordinary SetParameterValues, then
-- polls GetParameterValues until DiagnosticsState leaves "Requested".
-- That poll loop reuses QUEUED/RPC_SENT by cycling a job back to QUEUED
-- (Repository.Requeue) instead of finalizing it — see cmd/acs's
-- completeJob for the state machine.
ALTER TABLE jobs DROP CONSTRAINT jobs_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_type_check
    CHECK (type IN ('SET_PARAMETER', 'GET_PARAMETER', 'CONNECTION_REQUEST', 'FIRMWARE_DOWNLOAD', 'DIAGNOSTICS_PING'));
