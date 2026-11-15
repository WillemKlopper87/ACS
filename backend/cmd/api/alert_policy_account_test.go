package main

import (
	"encoding/json"
	"strings"
	"testing"

	"acs/internal/accounts"
)

// A tenant alert policy names an account. When two customers have an
// account of that ID the request must say whose, and the policy is tied
// to that one account.
func TestTenantAlertPolicyNamesOneCustomersAccount(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", "superadmin")
	custA, custB := e.customer("alert-isp-a"), e.customer("alert-isp-b")
	keys := map[string]string{}
	for _, c := range []string{custA, custB} {
		key, err := accounts.Ensure(accounts.WithCustomer(e.ctx, c), e.db, "7001")
		if err != nil {
			t.Fatalf("ensure account: %v", err)
		}
		keys[c] = key
	}
	body := func(extra map[string]any) map[string]any {
		b := map[string]any{"name": "isp policy", "scope": "tenant", "tenant_id": "7001"}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	if r := e.call("root", "POST", "/api/v1/alert-policies", body(nil)); r.code != 409 || !strings.Contains(r.body, "customer_id") {
		t.Fatalf("ambiguous account → %d %s, want 409 asking for customer_id", r.code, r.body)
	}
	if r := e.call("root", "POST", "/api/v1/alert-policies", body(map[string]any{"customer_id": "none"})); r.code != 400 {
		t.Fatalf("no unowned account 7001 → %d %s, want 400", r.code, r.body)
	}
	if r := e.call("root", "POST", "/api/v1/alert-policies", map[string]any{"name": "x", "scope": "fleet", "customer_id": custA}); r.code != 400 {
		t.Fatalf("customer_id on a fleet policy → %d %s, want 400", r.code, r.body)
	}
	r := e.call("root", "POST", "/api/v1/alert-policies", body(map[string]any{"customer_id": custA}))
	if r.code != 201 {
		t.Fatalf("create for customer A → %d %s", r.code, r.body)
	}
	var created struct{ ID, CustomerID string }
	_ = json.Unmarshal([]byte(strings.NewReplacer(`"id"`, `"ID"`, `"customer_id"`, `"CustomerID"`).Replace(r.body)), &created)
	if created.CustomerID != custA {
		t.Fatalf("created policy customer_id = %q, want %s (%s)", created.CustomerID, custA, r.body)
	}
	var stored string
	if err := e.db.QueryRowContext(e.ctx, `SELECT account_key::text FROM alert_policies WHERE id = $1`, created.ID).Scan(&stored); err != nil || stored != keys[custA] {
		t.Fatalf("stored account_key = %q (%v), want customer A's %s", stored, err, keys[custA])
	}
	// An ID no account has yet is still accepted, without a key.
	if r := e.call("root", "POST", "/api/v1/alert-policies", map[string]any{"name": "early", "scope": "tenant", "tenant_id": "not-yet"}); r.code != 201 {
		t.Fatalf("policy for an account that does not exist yet → %d %s, want 201", r.code, r.body)
	}
}
