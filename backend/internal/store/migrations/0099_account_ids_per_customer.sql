-- Per-ISP account IDs and order IDs, phase 2 of 2
-- (docs/TENANT-SCOPED-IDS-DESIGN.md).
--
-- The identifiers an integration chooses stop being unique across the
-- whole ACS. What 0098 prepared takes over:
--
--   account_id         unique per customer   (bss_accounts_scope_account_idx)
--   external_order_id  unique per customer   (bss_orders_scope_external_idx)
--   TMF641 externalId  unique per customer   (service_orders_scope_external_idx)
--   problem externalId unique per account    (tmf_service_problems_account_external_idx)
ALTER TABLE bss_accounts DROP CONSTRAINT bss_accounts_account_id_global_key;
ALTER TABLE bss_orders DROP CONSTRAINT bss_orders_external_order_id_global_key;
ALTER TABLE service_orders DROP CONSTRAINT service_orders_external_id_global_key;
ALTER TABLE tmf_service_problems DROP CONSTRAINT tmf_service_problems_external_id_global_key;
CREATE INDEX bss_accounts_account_id_idx ON bss_accounts (account_id);
CREATE INDEX bss_orders_external_order_id_idx ON bss_orders (external_order_id);

-- An event's or alarm's source key is chosen by whoever reports it, an
-- integration included (TMF688, TMF642), so it had the same problem: one
-- customer's key made another's identical key a silent no-op. Events are
-- unique per customer, alarms per account.
ALTER TABLE tmf_events DROP CONSTRAINT tmf_events_source_key_key;
CREATE UNIQUE INDEX tmf_events_scope_source_key_idx
    ON tmf_events ((COALESCE(customer_id::text, '')), source_key);
CREATE INDEX tmf_events_source_key_idx ON tmf_events (source_key);

ALTER TABLE tmf_alarms DROP CONSTRAINT tmf_alarms_source_key_key;
CREATE UNIQUE INDEX tmf_alarms_account_source_key_idx
    ON tmf_alarms ((COALESCE(account_key::text, '')), source_key);
CREATE INDEX tmf_alarms_source_key_idx ON tmf_alarms (source_key);

-- The device-event trigger's own duplicate check follows the new index.
-- (tmf_events_assign_sequence, a BEFORE INSERT trigger, has set the
-- customer by the time the conflict is checked.)
CREATE OR REPLACE FUNCTION emit_device_event(dev devices, event_type text, data jsonb) RETURNS void AS $$
DECLARE
    acct_id  text;
    acct_key uuid;
BEGIN
    SELECT account_id, account_key INTO acct_id, acct_key FROM account_device_mappings
     WHERE device_id = dev.id AND unassigned_at IS NULL ORDER BY assigned_at LIMIT 1;
    INSERT INTO tmf_events (id, source_key, account_id, account_key, device_id, event_type, event_time, payload)
    VALUES (gen_random_uuid(),
            'device-' || lower(event_type) || '-' || dev.id || '-' || (extract(epoch FROM clock_timestamp()) * 1000000)::bigint,
            acct_id, acct_key, dev.id, event_type, now(),
            data || jsonb_build_object('ouiSerial', dev.oui_serial))
    ON CONFLICT ((COALESCE(customer_id::text, '')), source_key) DO NOTHING;
END;
$$ LANGUAGE plpgsql;
