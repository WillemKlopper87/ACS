-- Separate database logins per service (docs/SECURITY-ASSESSMENT-2026-11-14.md, O5).
--
-- The event-stream triggers of 0090 read and write tables on behalf of
-- whichever service changed a device: the gateway updating a device row
-- makes the trigger read account_device_mappings and bss_accounts and
-- write tmf_events and tmf_event_sequences. With one shared login that
-- needed nothing. With a login per service, the gateway would have to be
-- granted those tables only so that a trigger could use them.
--
-- Instead the trigger functions run with the rights of their owner (the
-- schema owner), like a stored procedure with a fixed job. search_path is
-- pinned so the names inside them cannot be redirected to another schema.
ALTER FUNCTION tmf_events_assign_sequence() SECURITY DEFINER SET search_path = public, pg_temp;
ALTER FUNCTION devices_emit_events() SECURITY DEFINER SET search_path = public, pg_temp;
ALTER FUNCTION parameter_cache_emit_firmware() SECURITY DEFINER SET search_path = public, pg_temp;

-- emit_device_event is only ever called by the two triggers above, which
-- now call it as the owner. Nobody else needs to be able to write an
-- event of their choosing through it.
REVOKE EXECUTE ON FUNCTION emit_device_event(devices, text, jsonb) FROM PUBLIC;
