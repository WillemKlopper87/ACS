package main

import (
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
		if _, err := e.db.ExecContext(e.ctx, `INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, role, assigned_at)
			SELECT gen_random_uuid(), $1, id, oui_serial, 'gateway', now() FROM devices WHERE id = $2`, acc, dev); err != nil {
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
	if audits() != 1 {
		t.Fatalf("a serial lookup was audited")
	}
	if r := e.call("noc-a", "GET", "/api/v1/lookup", nil); r.code != 400 {
		t.Fatalf("empty lookup = %d", r.code)
	}

	// The device list carries the readable name.
	if r := e.call("noc-a", "GET", "/api/v1/devices/"+devA, nil); !strings.Contains(r.body, `"display_name":"Thandi Nkosi · ACC-A1`) {
		t.Fatalf("device = %s", r.body)
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
}
