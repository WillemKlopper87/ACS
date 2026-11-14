package store

import "testing"

// The registry migration runs on a database that already has accounts:
// some claimed by a customer, some never claimed, with mappings, orders,
// TMF order items, events and profiles. Every row must end up pointing at
// the right registry entry.
func TestMigration0098_BackfillsAccountKeys(t *testing.T) {
	ctx, db := preMigratedDB(t, "0097_service_role_triggers.sql")
	for _, q := range []string{
		`INSERT INTO customers (id, name) VALUES ('aaaaaaaa-0000-0000-0000-000000000001', 'ISP A')`,
		`INSERT INTO devices (id, oui_serial, customer_id) VALUES ('11111111-0000-0000-0000-000000000001', 'AAA-DEV-1', 'aaaaaaaa-0000-0000-0000-000000000001')`,
		`INSERT INTO devices (id, oui_serial) VALUES ('11111111-0000-0000-0000-000000000002', 'AAA-DEV-2')`,
		// An owned account and one nobody claimed.
		`INSERT INTO bss_accounts (account_id, customer_id, claimed_by) VALUES ('owned-1', 'aaaaaaaa-0000-0000-0000-000000000001', 'client-a')`,
		`INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, status, role) VALUES ('22222222-0000-0000-0000-000000000001', 'owned-1', '11111111-0000-0000-0000-000000000001', 'AAA-DEV-1', 'ACTIVE', 'gateway')`,
		`INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, status, role) VALUES ('22222222-0000-0000-0000-000000000002', 'legacy-1', '11111111-0000-0000-0000-000000000002', 'AAA-DEV-2', 'ACTIVE', 'gateway')`,
		`INSERT INTO bss_orders (external_order_id, account_id, action, command_key) VALUES ('ord-owned', 'owned-1', 'MODIFY_WIFI', 'ck-1')`,
		`INSERT INTO bss_orders (external_order_id, account_id, action, command_key) VALUES ('ord-legacy', 'legacy-1', 'MODIFY_WIFI', 'ck-2')`,
		`INSERT INTO service_orders (id, external_id, account_id, raw_request) VALUES ('33333333-0000-0000-0000-000000000001', 'so-1', 'owned-1', '{}')`,
		`INSERT INTO service_order_items (id, order_id, seq, action, external_order_id) VALUES ('44444444-0000-0000-0000-000000000001', '33333333-0000-0000-0000-000000000001', 0, 'modify', 'ord-owned')`,
		`INSERT INTO service_order_items (id, order_id, seq, action) VALUES ('44444444-0000-0000-0000-000000000002', '33333333-0000-0000-0000-000000000001', 1, 'noChange')`,
		`INSERT INTO account_profiles (account_id, display_name, name_source) VALUES ('profile-only', 'Someone', 'operator')`,
		`INSERT INTO webhook_subscriptions (id, account_id, target_url, secret, event_types) VALUES ('55555555-0000-0000-0000-000000000001', NULL, 'https://example.invalid/fleet', 's', '{DeviceSeen}')`,
		`INSERT INTO webhook_subscriptions (id, account_id, target_url, secret, event_types) VALUES ('55555555-0000-0000-0000-000000000002', 'owned-1', 'https://example.invalid/a', 's', '{DeviceSeen}')`,
		`INSERT INTO tmf_events (id, source_key, account_id, event_type, event_time) VALUES ('66666666-0000-0000-0000-000000000001', 'sk-1', 'owned-1', 'OrderCompleted', now())`,
		`INSERT INTO tmf_events (id, source_key, event_type, event_time) VALUES ('66666666-0000-0000-0000-000000000002', 'sk-2', 'DeviceSeen', now())`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	one := func(q string, args ...any) string {
		t.Helper()
		var v string
		if err := db.QueryRowContext(ctx, q, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return v
	}
	// The registry: the owned account kept its owner; the others were
	// registered as unowned.
	if got := one(`SELECT string_agg(account_id || ':' || COALESCE(customer_id::text, 'unowned'), ',' ORDER BY account_id) FROM bss_accounts`); got !=
		"legacy-1:unowned,owned-1:aaaaaaaa-0000-0000-0000-000000000001,profile-only:unowned" {
		t.Fatalf("registry = %s", got)
	}
	// Every row that names an account points at that account's entry.
	for _, table := range []string{"account_device_mappings", "bss_orders", "service_orders", "webhook_subscriptions", "tmf_events", "account_profiles"} {
		if n := one(`SELECT COUNT(*)::text FROM ` + table + ` c LEFT JOIN bss_accounts a ON a.id = c.account_key
			WHERE c.account_id IS NOT NULL AND a.account_id IS DISTINCT FROM c.account_id`); n != "0" {
			t.Errorf("%s: %s rows point at the wrong account", table, n)
		}
		if n := one(`SELECT COUNT(*)::text FROM ` + table + ` WHERE account_id IS NULL AND account_key IS NOT NULL`); n != "0" {
			t.Errorf("%s: %s rows without an account have a key", table, n)
		}
	}
	// Orders carry their owner as the scope of their ID, and the TMF item
	// points at its order by key.
	if got := one(`SELECT string_agg(external_order_id || ':' || order_scope, ',' ORDER BY external_order_id) FROM bss_orders`); got !=
		"ord-legacy:,ord-owned:aaaaaaaa-0000-0000-0000-000000000001" {
		t.Fatalf("order scopes = %s", got)
	}
	if got := one(`SELECT o.external_order_id FROM service_order_items i JOIN bss_orders o ON o.id = i.bss_order_id WHERE i.seq = 0`); got != "ord-owned" {
		t.Fatalf("order item points at %s", got)
	}
	if got := one(`SELECT COUNT(*)::text FROM service_order_items WHERE seq = 1 AND bss_order_id IS NULL`); got != "1" {
		t.Fatal("an item without an order gained one")
	}
	// Phase 1 keeps IDs unique across the ACS.
	if _, err := db.ExecContext(ctx, `INSERT INTO bss_accounts (account_id) VALUES ('owned-1')`); err == nil {
		t.Fatal("a second account with an existing ID was accepted")
	}
	// A row cannot name an account by ID without its key.
	if _, err := db.ExecContext(ctx, `INSERT INTO account_profiles (account_id) VALUES ('no-key')`); err == nil {
		t.Fatal("a row with an account ID and no key was accepted")
	}
}
