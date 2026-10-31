-- Event stream for integrations (I2, docs/integration/09-events.md).
--
-- Every tmf_events row gets the ACS customer it belongs to and a sequence
-- number that increases per customer, so an ISP can replay its own events
-- from a cursor. Both are assigned by a trigger, whatever wrote the event
-- (cmd/acs, cmd/uspc, cmd/bssadapter, the triggers below).
--
-- Ordering: the per-customer counter row stays locked until the inserting
-- transaction commits, so sequence numbers of one customer commit in order
-- and a reader never sees a lower number appear after a higher one. They
-- are strictly increasing, not gap-free: an insert that loses a source_key
-- race consumes a number. A gap therefore never means a lost event; only
-- purged_through (retention) does.

-- customer_id has no foreign key on purpose: an event stays in the stream
-- it was numbered in even if the customer is deleted later.
ALTER TABLE tmf_events
    ADD COLUMN customer_id UUID,
    ADD COLUMN seq BIGINT;

-- Events are history: every device that connects now has some, and they
-- must not stop the device being deleted.
ALTER TABLE tmf_events
    DROP CONSTRAINT tmf_events_device_id_fkey,
    ADD CONSTRAINT tmf_events_device_id_fkey FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE SET NULL;

CREATE TABLE tmf_event_sequences (
    scope TEXT PRIMARY KEY,                 -- customer id, '' for events with no customer
    last_seq BIGINT NOT NULL DEFAULT 0,
    purged_through BIGINT NOT NULL DEFAULT 0 -- events at or below this were removed by retention
);

-- Existing events: customer from the account owner, else the device.
UPDATE tmf_events e SET customer_id = COALESCE(
    (SELECT a.customer_id FROM bss_accounts a WHERE a.account_id = e.account_id),
    (SELECT d.customer_id FROM devices d WHERE d.id = e.device_id));
UPDATE tmf_events e SET seq = n.rn FROM (
    SELECT id, row_number() OVER (PARTITION BY COALESCE(customer_id::text, '') ORDER BY event_time, id) AS rn
      FROM tmf_events) n
 WHERE n.id = e.id;
INSERT INTO tmf_event_sequences (scope, last_seq)
SELECT COALESCE(customer_id::text, ''), max(seq) FROM tmf_events GROUP BY 1;

ALTER TABLE tmf_events ALTER COLUMN seq SET NOT NULL;
CREATE UNIQUE INDEX tmf_events_scope_seq_idx ON tmf_events ((COALESCE(customer_id::text, '')), seq);

CREATE FUNCTION tmf_events_assign_sequence() RETURNS trigger AS $$
DECLARE
    scope_key text;
BEGIN
    IF NEW.customer_id IS NULL AND NEW.account_id IS NOT NULL THEN
        SELECT customer_id INTO NEW.customer_id FROM bss_accounts WHERE account_id = NEW.account_id;
    END IF;
    IF NEW.customer_id IS NULL AND NEW.device_id IS NOT NULL THEN
        SELECT customer_id INTO NEW.customer_id FROM devices WHERE id = NEW.device_id;
    END IF;
    scope_key := COALESCE(NEW.customer_id::text, '');
    INSERT INTO tmf_event_sequences (scope, last_seq) VALUES (scope_key, 1)
    ON CONFLICT (scope) DO UPDATE SET last_seq = tmf_event_sequences.last_seq + 1
    RETURNING last_seq INTO NEW.seq;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER tmf_events_assign_sequence
    BEFORE INSERT ON tmf_events
    FOR EACH ROW EXECUTE FUNCTION tmf_events_assign_sequence();

-- Device events, emitted where the state changes so every writer is
-- covered. account_id is the device's current BSS mapping, if any, so
-- account-scoped webhook subscriptions receive them.
CREATE FUNCTION emit_device_event(dev devices, event_type text, data jsonb) RETURNS void AS $$
BEGIN
    INSERT INTO tmf_events (id, source_key, account_id, device_id, event_type, event_time, payload)
    VALUES (gen_random_uuid(),
            'device-' || lower(event_type) || '-' || dev.id || '-' || (extract(epoch FROM clock_timestamp()) * 1000000)::bigint,
            (SELECT account_id FROM account_device_mappings
              WHERE device_id = dev.id AND unassigned_at IS NULL ORDER BY assigned_at LIMIT 1),
            dev.id, event_type, now(),
            data || jsonb_build_object('ouiSerial', dev.oui_serial))
    ON CONFLICT (source_key) DO NOTHING;
END;
$$ LANGUAGE plpgsql;

-- DeviceSeen: first contact (the first Inform/connect ever), or back to
-- ONLINE from OFFLINE/UNREACHABLE. DeviceUnreachable: the liveness reaper
-- moved it to UNREACHABLE (no contact for the unreachable threshold, 90
-- minutes by default). Pre-registered devices that never connected emit
-- nothing until their first contact.
CREATE FUNCTION devices_emit_events() RETURNS trigger AS $$
BEGIN
    IF NEW.last_inform_at IS NOT NULL AND (TG_OP = 'INSERT' OR OLD.last_inform_at IS NULL) THEN
        PERFORM emit_device_event(NEW, 'DeviceSeen', jsonb_build_object(
            'firstContact', true, 'onlineStatus', NEW.online_status, 'lastInformAt', NEW.last_inform_at));
    ELSIF TG_OP = 'UPDATE' AND NEW.online_status = 'ONLINE' AND OLD.online_status IN ('OFFLINE', 'UNREACHABLE') THEN
        PERFORM emit_device_event(NEW, 'DeviceSeen', jsonb_build_object(
            'firstContact', false, 'onlineStatus', NEW.online_status, 'previousStatus', OLD.online_status,
            'lastInformAt', NEW.last_inform_at));
    ELSIF TG_OP = 'UPDATE' AND NEW.online_status = 'UNREACHABLE' AND OLD.online_status IS DISTINCT FROM 'UNREACHABLE' THEN
        PERFORM emit_device_event(NEW, 'DeviceUnreachable', jsonb_build_object(
            'onlineStatus', NEW.online_status, 'previousStatus', OLD.online_status, 'lastInformAt', NEW.last_inform_at));
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER devices_emit_events
    AFTER INSERT OR UPDATE OF online_status, last_inform_at ON devices
    FOR EACH ROW EXECUTE FUNCTION devices_emit_events();

-- FirmwareChanged: the cached SoftwareVersion moved from one known value
-- to another (learning it the first time is not a change).
CREATE FUNCTION software_version(params jsonb) RETURNS text AS $$
    SELECT COALESCE(params -> 'Device.DeviceInfo.SoftwareVersion' ->> 'value',
                    params -> 'InternetGatewayDevice.DeviceInfo.SoftwareVersion' ->> 'value');
$$ LANGUAGE sql IMMUTABLE;

CREATE FUNCTION parameter_cache_emit_firmware() RETURNS trigger AS $$
DECLARE
    old_version text := software_version(OLD.parameters);
    new_version text := software_version(NEW.parameters);
    dev devices;
BEGIN
    IF old_version IS NOT NULL AND new_version IS NOT NULL AND old_version <> new_version THEN
        SELECT * INTO dev FROM devices WHERE id = NEW.device_id;
        PERFORM emit_device_event(dev, 'FirmwareChanged', jsonb_build_object(
            'previousVersion', old_version, 'version', new_version));
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER parameter_cache_emit_firmware
    AFTER UPDATE OF parameters ON device_parameter_cache
    FOR EACH ROW EXECUTE FUNCTION parameter_cache_emit_firmware();

-- Machine-readable reason for an onboarding's state (docs/ONBOARDING.md).
ALTER TABLE onboardings ADD COLUMN state_code TEXT;

-- Webhook secret rotation: during an overlap the previous secret signs too.
ALTER TABLE webhook_subscriptions
    ADD COLUMN previous_secret TEXT,
    ADD COLUMN previous_secret_expires_at TIMESTAMPTZ;

-- Delivery view: the last error of a delivery attempt.
ALTER TABLE webhook_deliveries ADD COLUMN last_error TEXT;
