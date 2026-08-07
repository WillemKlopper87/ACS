-- Phase 2: the session needs to remember which job's RPC it is currently
-- waiting on, so the next incoming POST on that session is known to be
-- that job's response rather than an ambiguous "empty poll" (build plan
-- §4 Phase 2 "serial RPC dispatch" / design doc v3 §5.4 one-in-flight-RPC
-- model, now backed by Postgres instead of the Phase 0 in-memory session).
ALTER TABLE cwmp_sessions ADD COLUMN current_job_id UUID REFERENCES jobs(id);
