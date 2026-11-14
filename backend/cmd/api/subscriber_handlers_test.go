package main

import (
	"acs/internal/accounts/accountstest"
	"strings"
	"testing"

	"acs/internal/operators"
	"acs/internal/subscribers"
	"acs/internal/tenancy"
)

// TestIntegration_SubscriberLookup: scoped lookup, readable device names,
// audited name searches, and the profile rules (operators fill a missing
// name, never replace the BSS's; VIP needs subscriber.manage).
func TestIntegration_SubscriberLookup(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("ISP A"), e.customer("ISP B")
	e.operator("noc-a", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.operator("mgr-a", operators.RoleManager, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	devA := e.device("LK0001A1B2", &custA)
	devB := e.device("LK0002C3D4", &custB)
	for acc, dev := range map[string]string{"ACC-A1": devA, "ACC-B1": devB} {
		if _, err := e.db.ExecContext(e.ctx, accountstest.EnsureCTE("$1")+`INSERT INTO account_device_mappings (id, account_id, account_key, device_id, oui_serial, role, assigned_at)
			SELECT gen_random_uuid(), $1, (SELECT id FROM acct), id, oui_serial, 'gateway', now() FROM devices WHERE id = $2`, acc, dev); err != nil {
			t.Fatal(err)
		}
	}
	name := func(s string) *string { return &s }
	for acc, n := range map[string]string{"ACC-A1": "Thandi Nkosi", "ACC-B1": "Thandi Nkosi"} {
		if _, err := e.h.subscribers.Set(e.ctx, acc, subscribers.SourceBSS, "bss", name(n), nil); err != nil {
			t.Fatal(err)
		}
	}
	audits := func() int {
		var n int
		_ = e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM audit_log WHERE action = 'SubscriberLookup'`).Scan(&n)
		return n
	}

	r := e.call("noc-a", "GET", "/api/v1/lookup?q=thandi", nil)
	if r.code != 200 || !strings.Contains(r.body, devA) || strings.Contains(r.body, devB) || !strings.Contains(r.body, `"matched":["name"]`) {
		t.Fatalf("name lookup = %d %s", r.code, r.body)
	}
	if audits() != 1 {
		t.Fatalf("a name lookup was not audited")
	}
	if r := e.call("noc-a", "GET", "/api/v1/lookup?q=a1b2", nil); r.code != 200 || !strings.Contains(r.body, devA) {
		t.Fatalf("serial digits = %d %s", r.code, r.body)
	}
	if audits() != 2 {
		t.Fatalf("a serial lookup was not audited (it returns names too)")
	}
	if r := e.call("noc-a", "GET", "/api/v1/lookup", nil); r.code != 400 {
		t.Fatalf("empty lookup = %d", r.code)
	}

	// The device list carries the readable name.
	if r := e.call("noc-a", "GET", "/api/v1/devices/"+devA, nil); !strings.Contains(r.body, `"display_name":"Thandi Nkosi · ACC-A1`) {
		t.Fatalf("device = %s", r.body)
	}

	// Device reads give the area, not the home; the street comes only from
	// the audited reveal, and only for a device in the caller's scope.
	if _, err := e.db.ExecContext(e.ctx, `WITH s AS (INSERT INTO sites (id, customer_id, name, street, suburb, city)
		VALUES (gen_random_uuid(), $1, 'Home', '12 Rivonia Rd', 'Sandton', 'Johannesburg') RETURNING id)
		UPDATE devices SET site_id = (SELECT id FROM s) WHERE id = $2`, custA, devA); err != nil {
		t.Fatal(err)
	}
	if r := e.call("noc-a", "GET", "/api/v1/devices/"+devA, nil); !strings.Contains(r.body, "Sandton") || strings.Contains(r.body, "Rivonia") {
		t.Fatalf("device read shows the street: %s", r.body)
	}
	if r := e.call("noc-a", "GET", "/api/v1/devices", nil); strings.Contains(r.body, "Rivonia") {
		t.Fatalf("device list shows the street: %s", r.body)
	}
	revealed := func() int {
		var n int
		_ = e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM audit_log WHERE action = 'SubscriberDetailsRevealed'`).Scan(&n)
		return n
	}
	if r := e.call("noc-a", "POST", "/api/v1/devices/"+devA+"/subscriber/reveal", nil); r.code != 200 || !strings.Contains(r.body, "12 Rivonia Rd") {
		t.Fatalf("reveal = %d %s", r.code, r.body)
	}
	if revealed() != 1 {
		t.Fatalf("a reveal was not audited")
	}

	// Profiles: NOC may read, not write; a manager sets VIP but cannot
	// replace the BSS name; another customer's account is invisible.
	if r := e.call("noc-a", "PUT", "/api/v1/accounts/ACC-A1/profile", map[string]any{"vip": true}); r.code != 403 {
		t.Fatalf("noc sets vip = %d", r.code)
	}
	if r := e.call("mgr-a", "PUT", "/api/v1/accounts/ACC-A1/profile", map[string]any{"vip": true}); r.code != 200 || !strings.Contains(r.body, `"vip":true`) {
		t.Fatalf("manager sets vip = %d %s", r.code, r.body)
	}
	if r := e.call("mgr-a", "PUT", "/api/v1/accounts/ACC-A1/profile", map[string]any{"subscriber_name": "Someone"}); r.code != 409 {
		t.Fatalf("manager replaces BSS name = %d", r.code)
	}
	if r := e.call("mgr-a", "PUT", "/api/v1/accounts/ACC-B1/profile", map[string]any{"vip": true}); r.code != 404 {
		t.Fatalf("other customer's account = %d", r.code)
	}
	if r := e.call("noc-a", "GET", "/api/v1/lookup?vip=1", nil); !strings.Contains(r.body, devA) || !strings.Contains(r.body, `"vip":true`) {
		t.Fatalf("vip list = %s", r.body)
	}

	// A device of customer B mapped onto A's owned account neither shows
	// A's subscriber to B nor lets B edit A's profile.
	if _, err := e.db.ExecContext(e.ctx, `INSERT INTO bss_accounts (account_id, customer_id, claimed_by) VALUES ('ACC-A1', $1, 'test') ON CONFLICT (account_id) DO UPDATE SET customer_id = EXCLUDED.customer_id`, custA); err != nil {
		t.Fatal(err)
	}
	e.operator("mgr-b", operators.RoleManager, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custB})
	devB2 := e.device("LK0003E5F6", &custB)
	if _, err := e.db.ExecContext(e.ctx, accountstest.EnsureCTE("'ACC-A1'")+`INSERT INTO account_device_mappings (id, account_id, account_key, device_id, oui_serial, role, assigned_at)
		SELECT gen_random_uuid(), 'ACC-A1', (SELECT id FROM acct), id, oui_serial, 'extender', now() FROM devices WHERE id = $1`, devB2); err != nil {
		t.Fatal(err)
	}
	if r := e.call("mgr-b", "PUT", "/api/v1/accounts/ACC-A1/profile", map[string]any{"vip": false}); r.code != 404 {
		t.Fatalf("B edits A's owned account through its own device = %d", r.code)
	}
	if r := e.call("mgr-b", "GET", "/api/v1/devices/"+devB2, nil); r.code != 200 || strings.Contains(r.body, "Thandi") || strings.Contains(r.body, `"vip":true`) {
		t.Fatalf("B's device shows A's subscriber: %d %s", r.code, r.body)
	}
	if r := e.call("mgr-b", "POST", "/api/v1/devices/"+devA+"/subscriber/reveal", nil); r.code != 404 || revealed() != 1 {
		t.Fatalf("B reveals A's device = %d", r.code)
	}
	if r := e.call("mgr-b", "POST", "/api/v1/devices/"+devB2+"/subscriber/reveal", nil); strings.Contains(r.body, "Thandi") {
		t.Fatalf("B's reveal shows A's subscriber: %s", r.body)
	}
	if r := e.call("mgr-b", "GET", "/api/v1/lookup?q=e5f6", nil); strings.Contains(r.body, "Thandi") {
		t.Fatalf("B's lookup shows A's subscriber: %s", r.body)
	}

	// The same for an unowned account shared by both customers' devices:
	// neither sees a name, and neither may edit it.
	devA2, devB3 := e.device("LK0004AAAA", &custA), e.device("LK0005BBBB", &custB)
	for _, dev := range []string{devA2, devB3} {
		if _, err := e.db.ExecContext(e.ctx, accountstest.EnsureCTE("'ACC-SHARED'")+`INSERT INTO account_device_mappings (id, account_id, account_key, device_id, oui_serial, role, assigned_at)
			SELECT gen_random_uuid(), 'ACC-SHARED', (SELECT id FROM acct), id, oui_serial, CASE WHEN customer_id = $2 THEN 'gateway' ELSE 'extender' END, now() FROM devices WHERE id = $1`, dev, custA); err != nil {
			t.Fatal(err)
		}
	}
	shared := "Shared Name"
	if _, err := e.h.subscribers.Set(e.ctx, "ACC-SHARED", subscribers.SourceOperator, "op", &shared, nil); err != nil {
		t.Fatal(err)
	}
	for op, dev := range map[string]string{"mgr-a": devA2, "mgr-b": devB3} {
		if r := e.call(op, "GET", "/api/v1/devices/"+dev, nil); strings.Contains(r.body, shared) {
			t.Fatalf("%s sees a shared unowned account's name: %s", op, r.body)
		}
		if r := e.call(op, "PUT", "/api/v1/accounts/ACC-SHARED/profile", map[string]any{"vip": true}); r.code != 404 {
			t.Fatalf("%s edits a shared unowned account = %d", op, r.code)
		}
	}
}
