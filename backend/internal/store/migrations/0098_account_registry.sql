-- Per-ISP account IDs and order IDs, phase 1 of 2
-- (docs/TENANT-SCOPED-IDS-DESIGN.md).
--
-- bss_accounts becomes the registry of every account, owned or not, with
-- an internal key. Every table that names an account points at that key
-- and is looked up by it; the ISP's own account_id stays beside it as the
-- value shown and sent on the wire.
--
-- This phase changes no behaviour: account_id and external_order_id are
-- still unique across the whole ACS (the *_global_* constraints below),
-- so the backfill by account_id is unambiguous. Phase 2 drops those
-- constraints, after which the per-customer ones created here are what
-- remains.

-- 1. The registry ---------------------------------------------------------
ALTER TABLE bss_accounts DROP CONSTRAINT bss_accounts_pkey;
ALTER TABLE bss_accounts
    ADD COLUMN id UUID NOT NULL DEFAULT gen_random_uuid(),
    ALTER COLUMN customer_id DROP NOT NULL,          -- NULL: nobody owns it
    ALTER COLUMN claimed_by SET DEFAULT 'registry';
ALTER TABLE bss_accounts ADD PRIMARY KEY (id);
-- Phase 1 only: one namespace, as before.
ALTER TABLE bss_accounts ADD CONSTRAINT bss_accounts_account_id_global_key UNIQUE (account_id);
-- What phase 2 keeps: an ID is unique within its customer, and within the
-- unowned accounts.
CREATE UNIQUE INDEX bss_accounts_scope_account_idx
    ON bss_accounts ((COALESCE(customer_id::text, '')), account_id);

-- Every account any table mentions gets a registry row. Those that had
-- none were never claimed: they are unowned.
INSERT INTO bss_accounts (account_id, customer_id, claimed_by)
SELECT DISTINCT s.account_id, NULL::uuid, 'migration-0098'
  FROM (      SELECT account_id FROM account_device_mappings
    UNION ALL SELECT account_id FROM bss_orders
    UNION ALL SELECT account_id FROM service_orders
    UNION ALL SELECT account_id FROM webhook_subscriptions
    UNION ALL SELECT account_id FROM tmf_alarms
    UNION ALL SELECT account_id FROM tmf_events
    UNION ALL SELECT account_id FROM tmf_service_problems
    UNION ALL SELECT account_id FROM bss_idempotency_keys
    UNION ALL SELECT account_id FROM account_profiles
    UNION ALL SELECT account_id FROM account_profile_archive) s
 WHERE s.account_id IS NOT NULL
ON CONFLICT (account_id) DO NOTHING;

-- 2. account_key on every table that names an account ---------------------
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'account_device_mappings', 'bss_orders', 'service_orders', 'webhook_subscriptions',
        'tmf_alarms', 'tmf_events', 'tmf_service_problems', 'bss_idempotency_keys',
        'account_profiles', 'account_profile_archive']
    LOOP
        EXECUTE format('ALTER TABLE %I ADD COLUMN account_key UUID REFERENCES bss_accounts(id)', t);
        EXECUTE format('UPDATE %I c SET account_key = a.id FROM bss_accounts a WHERE a.account_id = c.account_id', t);
        -- The two always go together: a row names an account by both or
        -- by neither (a fleet-wide webhook, an event of an unmapped device).
        EXECUTE format('ALTER TABLE %I ADD CONSTRAINT %I CHECK ((account_id IS NULL) = (account_key IS NULL))',
                       t, t || '_account_key_ck');
        EXECUTE format('CREATE INDEX %I ON %I (account_key)', t || '_account_key_idx', t);
    END LOOP;
END
$$;

-- 3. Keys and uniqueness move from the text ID to the key -----------------
DROP INDEX account_device_mappings_active_idx;
DROP INDEX account_device_mappings_active_role_idx;
CREATE UNIQUE INDEX account_device_mappings_active_idx
    ON account_device_mappings (account_key, device_id) WHERE unassigned_at IS NULL;
CREATE UNIQUE INDEX account_device_mappings_active_role_idx
    ON account_device_mappings (account_key, role) WHERE unassigned_at IS NULL;

ALTER TABLE bss_idempotency_keys DROP CONSTRAINT bss_idempotency_keys_pkey;
ALTER TABLE bss_idempotency_keys ADD PRIMARY KEY (account_key, key);

ALTER TABLE account_profiles DROP CONSTRAINT account_profiles_pkey;
ALTER TABLE account_profiles ADD PRIMARY KEY (account_key);
DROP INDEX account_profiles_vip_idx;
CREATE INDEX account_profiles_vip_idx ON account_profiles (account_key) WHERE vip;

DROP INDEX tmf_alarms_account_state_idx;
CREATE INDEX tmf_alarms_account_state_idx ON tmf_alarms (account_key, state);
DROP INDEX tmf_events_account_time_idx;
CREATE INDEX tmf_events_account_time_idx ON tmf_events (account_key, event_time DESC);
DROP INDEX tmf_service_problems_account_status_idx;
CREATE INDEX tmf_service_problems_account_status_idx ON tmf_service_problems (account_key, status);
-- A problem's external ID belongs to its account.
CREATE UNIQUE INDEX tmf_service_problems_account_external_idx
    ON tmf_service_problems (account_key, external_id) WHERE external_id IS NOT NULL;
ALTER TABLE tmf_service_problems
    RENAME CONSTRAINT tmf_service_problems_external_id_key TO tmf_service_problems_external_id_global_key;

-- 4. Orders ----------------------------------------------------------------
-- An order gets an internal key; the ISP's external_order_id will be
-- unique per ISP. order_scope is the owning customer ('' when unowned),
-- copied from the account so that uniqueness can be an index; the one
-- place that moves an account between customers updates it.
ALTER TABLE service_order_items DROP CONSTRAINT service_order_items_external_order_id_fkey;
ALTER TABLE bss_orders DROP CONSTRAINT bss_orders_pkey;
ALTER TABLE bss_orders
    ADD COLUMN id UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN order_scope TEXT NOT NULL DEFAULT '',
    -- A digest of the order as first accepted (finding O7). Recorded, not
    -- yet enforced; NULL on orders from before this migration.
    ADD COLUMN request_hash TEXT;
ALTER TABLE bss_orders ADD PRIMARY KEY (id);
ALTER TABLE bss_orders ADD CONSTRAINT bss_orders_external_order_id_global_key UNIQUE (external_order_id);
UPDATE bss_orders o SET order_scope = COALESCE(a.customer_id::text, '')
  FROM bss_accounts a WHERE a.id = o.account_key;
CREATE UNIQUE INDEX bss_orders_scope_external_idx ON bss_orders (order_scope, external_order_id);

ALTER TABLE service_order_items ADD COLUMN bss_order_id UUID REFERENCES bss_orders(id);
UPDATE service_order_items i SET bss_order_id = o.id
  FROM bss_orders o WHERE o.external_order_id = i.external_order_id;
ALTER TABLE service_order_items ADD CONSTRAINT service_order_items_bss_order_ck
    CHECK ((external_order_id IS NULL) = (bss_order_id IS NULL));
CREATE INDEX service_order_items_bss_order_idx ON service_order_items (bss_order_id) WHERE bss_order_id IS NOT NULL;

ALTER TABLE service_orders ADD COLUMN order_scope TEXT NOT NULL DEFAULT '';
UPDATE service_orders o SET order_scope = COALESCE(a.customer_id::text, '')
  FROM bss_accounts a WHERE a.id = o.account_key;
ALTER TABLE service_orders
    RENAME CONSTRAINT service_orders_external_id_key TO service_orders_external_id_global_key;
CREATE UNIQUE INDEX service_orders_scope_external_idx ON service_orders (order_scope, external_id);

-- 5. The event-stream triggers of 0090 name accounts by key ---------------
CREATE OR REPLACE FUNCTION tmf_events_assign_sequence() RETURNS trigger AS $$
DECLARE
    scope_key text;
BEGIN
    IF NEW.customer_id IS NULL AND NEW.account_key IS NOT NULL THEN
        SELECT customer_id INTO NEW.customer_id FROM bss_accounts WHERE id = NEW.account_key;
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
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp;

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
    ON CONFLICT (source_key) DO NOTHING;
END;
$$ LANGUAGE plpgsql;
