package main

import (
	"strings"
	"testing"

	"acs/internal/operators"
)

// Account IDs are unique per customer (docs/TENANT-SCOPED-IDS-DESIGN.md).
// An operator addressing an account by its ID says whose it is when two
// customers use the ID, and is never handed one of them by guesswork.
func TestIntegration_OperatorNamesTheCustomerOfASharedAccountID(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("ISP A"), e.customer("ISP B")
	e.operator("root", operators.RoleSuperAdmin)
	devA, devB := e.device("TI0001A", &custA), e.device("TI0002B", &custB)
	serial := func(id string) string {
		var s string
		if err := e.db.QueryRowContext(e.ctx, `SELECT oui_serial FROM devices WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// The same account ID for both customers, each with its own device.
	for cust, dev := range map[string]string{custA: devA, custB: devB} {
		r := e.call("root", "POST", "/api/v1/bss/mappings?customer_id="+cust, map[string]any{"account_id": "5001", "oui_serial": serial(dev)})
		if r.code/100 != 2 {
			t.Fatalf("map 5001 for %s = %d %s", cust, r.code, r.body)
		}
	}
	// The list says whose each account is.
	if r := e.call("root", "GET", "/api/v1/bss/mappings", nil); !strings.Contains(r.body, `"account_customer_id":"`+custA+`"`) || !strings.Contains(r.body, `"account_customer_id":"`+custB+`"`) {
		t.Fatalf("mapping list does not name both customers: %s", r.body)
	}

	// Unqualified, "5001" is two accounts: refused, and nothing changes.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/accounts/5001/profile", nil},
		{"PUT", "/api/v1/accounts/5001/profile", map[string]any{"vip": true}},
		{"GET", "/api/v1/bss/mappings/5001/history", nil},
		{"DELETE", "/api/v1/bss/mappings/5001/gateway?reason=corrected", nil},
		{"PUT", "/api/v1/bss/accounts/5001/customer", map[string]any{"customer_id": custA}},
	} {
		if r := e.call("root", c.method, c.path, c.body); r.code != 409 || !strings.Contains(r.body, "customer_id") {
			t.Fatalf("%s %s unqualified = %d %s, want 409 naming customer_id", c.method, c.path, r.code, r.body)
		}
	}
	var active int
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM account_device_mappings WHERE account_id = '5001' AND unassigned_at IS NULL`).Scan(&active); err != nil || active != 2 {
		t.Fatalf("active mappings of 5001 after refused calls = %d (%v), want 2", active, err)
	}

	// Named, each call reaches that customer's account only.
	if r := e.call("root", "PUT", "/api/v1/accounts/5001/profile?customer_id="+custA, map[string]any{"subscriber_name": "Asha", "vip": true}); r.code != 200 {
		t.Fatalf("name A's subscriber = %d %s", r.code, r.body)
	}
	if r := e.call("root", "GET", "/api/v1/accounts/5001/profile?customer_id="+custB, nil); r.code != 200 || strings.Contains(r.body, "Asha") {
		t.Fatalf("B's profile = %d %s, want it without A's subscriber", r.code, r.body)
	}
	if r := e.call("root", "GET", "/api/v1/bss/mappings/5001/history?customer_id="+custB, nil); r.code != 200 || !strings.Contains(r.body, serial(devB)) || strings.Contains(r.body, serial(devA)) {
		t.Fatalf("B's history = %d %s", r.code, r.body)
	}
	if r := e.call("root", "DELETE", "/api/v1/bss/mappings/5001/gateway?reason=corrected&customer_id="+custB, nil); r.code/100 != 2 {
		t.Fatalf("unassign B's = %d %s", r.code, r.body)
	}
	var owner string
	if err := e.db.QueryRowContext(e.ctx, `SELECT a.customer_id::text FROM account_device_mappings m JOIN bss_accounts a ON a.id = m.account_key
		WHERE m.account_id = '5001' AND m.unassigned_at IS NULL`).Scan(&owner); err != nil || owner != custA {
		t.Fatalf("the mapping left active belongs to %q (%v), want A's", owner, err)
	}

	// An account cannot be moved onto an ID the customer already has.
	if r := e.call("root", "PUT", "/api/v1/bss/accounts/5001/customer?customer_id="+custB, map[string]any{"customer_id": custA}); r.code != 409 || !strings.Contains(r.body, "already has") {
		t.Fatalf("move B's 5001 onto A's = %d %s, want 409", r.code, r.body)
	}
	// "none" is the account nobody owns: there is none of this ID.
	if r := e.call("root", "GET", "/api/v1/bss/mappings/5001/history?customer_id=none", nil); r.code != 200 || strings.Contains(r.body, "TI000") {
		t.Fatalf("unowned 5001 history = %d %s, want empty", r.code, r.body)
	}
}
