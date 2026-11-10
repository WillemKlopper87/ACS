package main

import (
	"database/sql"
	"strings"
	"testing"

	"acs/internal/bss"
	"acs/internal/operators"
	"acs/internal/subscribers"
	"acs/internal/tenancy"
)

// TestIntegration_SaleLifecycle: ending a service releases the device and
// archives a subscriber with no device left; a swap keeps the subscriber
// and records the replacement's sale; a sale moves to another site or
// plan of its own customer only.
func TestIntegration_SaleLifecycle(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("ISP A"), e.customer("ISP B")
	e.operator("mgr-a", operators.RoleManager, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.operator("noc-a", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.operator("mgr-b", operators.RoleManager, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custB})
	e.operator("root", operators.RoleSuperAdmin)

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.db.ExecContext(e.ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	one := func(q string, args ...any) (s sql.NullString) {
		t.Helper()
		if err := e.db.QueryRowContext(e.ctx, q, args...).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	site := func(customer string) (id string) {
		t.Helper()
		if err := e.db.QueryRowContext(e.ctx, `INSERT INTO sites (id, customer_id, name, street, suburb) VALUES (gen_random_uuid(), $1, 'Home', '1 Main Rd', 'Sandton') RETURNING id`, customer).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	plan := func(customer, code string, configures bool) (id string) {
		t.Helper()
		templates := "{}"
		if configures {
			templates = "{" + "6f1c0d4e-1111-4222-8333-944455556666" + "}"
		}
		if err := e.db.QueryRowContext(e.ctx, `INSERT INTO service_plans (id, customer_id, code, name, template_ids) VALUES (gen_random_uuid(), $1, $2, $2, $3) RETURNING id`,
			customer, code, templates).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	siteA, planA := site(custA), plan(custA, "HOME", false)
	// inService records a sale in service for a new device of customer A,
	// mapped to account as gateway, with a nickname and a project.
	inService := func(serial, account string, role ...string) (sale, device string) {
		t.Helper()
		device = e.device(serial, &custA)
		exec(`UPDATE devices SET site_id = $2, label = 'Lounge', location = 'Upstairs' WHERE id = $1`, device, siteA)
		if err := e.db.QueryRowContext(e.ctx, `INSERT INTO onboardings (id, customer_id, device_id, expected_serial, account_ref, site_id, service_plan_id,
			role, source, state, in_service_at) VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, 'gateway', 'console', 'IN_SERVICE', now()) RETURNING id`,
			custA, device, serial, account, siteA, planA).Scan(&sale); err != nil {
			t.Fatal(err)
		}
		mapRole := "gateway"
		if len(role) > 0 {
			mapRole = role[0]
		}
		exec(`INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, role, service_plan, status, assigned_at)
			SELECT gen_random_uuid(), $1, id, oui_serial, $3, 'HOME', 'ACTIVE', now() FROM devices WHERE id = $2`, account, device, mapRole)
		return sale, device
	}
	name := "Thandi Nkosi"
	vip := true

	// --- End service -----------------------------------------------------
	sale, dev := inService("LC0001", "ACC-END")
	if _, err := e.h.subscribers.Set(e.ctx, "ACC-END", subscribers.SourceBSS, "bss", &name, &vip); err != nil {
		t.Fatal(err)
	}
	if r := e.call("noc-a", "POST", "/api/v1/onboardings/"+sale+"/end", map[string]any{"reason": "return"}); r.code != 403 {
		t.Fatalf("noc ends a service = %d", r.code)
	}
	if r := e.call("mgr-b", "POST", "/api/v1/onboardings/"+sale+"/end", map[string]any{"reason": "return"}); r.code != 404 {
		t.Fatalf("another customer ends it = %d", r.code)
	}
	if r := e.call("mgr-a", "POST", "/api/v1/onboardings/"+sale+"/end", map[string]any{"reason": "churn"}); r.code != 400 {
		t.Fatalf("unknown reason = %d", r.code)
	}
	r := e.call("mgr-a", "POST", "/api/v1/onboardings/"+sale+"/end", map[string]any{"reason": "return", "factory_reset": true})
	if r.code != 200 || !strings.Contains(r.body, `"subscribers_archived":["ACC-END"]`) || !strings.Contains(r.body, `"factory_reset":"queued"`) {
		t.Fatalf("end = %d %s", r.code, r.body)
	}
	if s := one(`SELECT state || ':' || (ended_at IS NOT NULL)::text FROM onboardings WHERE id = $1`, sale); s.String != "ENDED:true" {
		t.Fatalf("sale = %s", s.String)
	}
	if s := one(`SELECT unassign_reason FROM account_device_mappings WHERE device_id = $1`, dev); s.String != "return" {
		t.Fatalf("mapping reason = %v", s)
	}
	if s := one(`SELECT concat_ws(',', customer_id, site_id, label, location) FROM devices WHERE id = $1`, dev); s.String != "" {
		t.Fatalf("device not released: %q", s.String)
	}
	if s := one(`SELECT count(*)::text FROM account_profiles WHERE account_id = 'ACC-END'`); s.String != "0" {
		t.Fatalf("profile still live")
	}
	if s := one(`SELECT display_name || ':' || vip::text FROM account_profile_archive WHERE account_id = 'ACC-END'`); s.String != "Thandi Nkosi:true" {
		t.Fatalf("archive = %q", s.String)
	}
	if s := one(`SELECT count(*)::text FROM jobs WHERE device_id = $1 AND type = 'FACTORY_RESET'`, dev); s.String != "1" {
		t.Fatalf("factory reset jobs = %s", s.String)
	}
	if s := one(`SELECT (reenroll_until > now())::text FROM devices WHERE id = $1`, dev); s.String != "true" {
		t.Fatalf("re-enrolment window not open")
	}
	if s := one(`SELECT count(*)::text FROM audit_log WHERE action = 'OnboardingEnded'`); s.String != "1" {
		t.Fatalf("end not audited")
	}
	// The released device is back in the pool of claimable devices; a device
	// whose sale is still in service is not.
	inService("LC0099", "ACC-LIVE")
	if r := e.call("root", "GET", "/api/v1/onboardings/unclaimed", nil); r.code != 200 || !strings.Contains(r.body, "LC0001") || strings.Contains(r.body, "LC0099") {
		t.Fatalf("unclaimed after an end: %d %s", r.code, r.body)
	}
	if r := e.call("mgr-a", "POST", "/api/v1/onboardings/"+sale+"/end", map[string]any{"reason": "return"}); r.code != 404 {
		// The device left customer A's scope with the release, so the
		// ended sale (still customer A's) answers, but not in service.
		if r.code != 409 {
			t.Fatalf("end twice = %d", r.code)
		}
	}
	// The BSS sync never maps an ended sale's device again.
	pending, err := bss.NewRepository(e.db).PreProvisionedNeedingSync(e.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pending {
		if p.OnboardingID == sale {
			t.Fatalf("ended sale queued for mapping sync")
		}
	}

	// An account with another device keeps its subscriber.
	saleK, _ := inService("LC0002", "ACC-TWO")
	inService("LC0003", "ACC-TWO", "extender")
	if _, err := e.h.subscribers.Set(e.ctx, "ACC-TWO", subscribers.SourceBSS, "bss", &name, nil); err != nil {
		t.Fatal(err)
	}
	if r := e.call("mgr-a", "POST", "/api/v1/onboardings/"+saleK+"/end", map[string]any{"reason": "corrected"}); r.code != 200 || !strings.Contains(r.body, `"subscribers_archived":[]`) {
		t.Fatalf("end with another device left = %d %s", r.code, r.body)
	}

	// --- Swap -------------------------------------------------------------
	saleS, devS := inService("LC0004", "ACC-SWAP")
	if _, err := e.h.subscribers.Set(e.ctx, "ACC-SWAP", subscribers.SourceBSS, "bss", &name, nil); err != nil {
		t.Fatal(err)
	}
	if r := e.call("mgr-a", "POST", "/api/v1/onboardings/"+saleS+"/swap", map[string]any{"reason": "return", "serial": "LC9999"}); r.code != 400 {
		t.Fatalf("swap for a return = %d", r.code)
	}
	r = e.call("mgr-a", "POST", "/api/v1/onboardings/"+saleS+"/swap", map[string]any{"reason": "rma", "serial": "LC9999"})
	if r.code != 200 || !strings.Contains(r.body, `"state":"PLANNED"`) || !strings.Contains(r.body, `"expected_serial":"LC9999"`) {
		t.Fatalf("swap = %d %s", r.code, r.body)
	}
	if s := one(`SELECT concat_ws(',', account_ref, (site_id::text = $1)::text, bss_role, bss_service_plan) FROM onboardings WHERE expected_serial = 'LC9999'`, siteA); s.String != "ACC-SWAP,true,gateway,HOME" {
		t.Fatalf("replacement sale = %q", s.String)
	}
	// The replacement is announced like any new sale (PLANNED), so an
	// integration can learn its id; the ended sale has its ENDED event.
	if s := one(`SELECT count(*)::text FROM tmf_events WHERE event_type = 'OnboardingStateChangeEvent'
	               AND payload->>'serialNumber' = 'LC9999' AND payload->>'state' = 'PLANNED' AND payload->>'previousState' = ''`); s.String != "1" {
		t.Fatalf("PLANNED events for the replacement sale = %s", s.String)
	}
	if s := one(`SELECT count(*)::text FROM tmf_events WHERE event_type = 'OnboardingStateChangeEvent'
	               AND payload->>'serialNumber' = 'LC0004' AND payload->>'state' = 'ENDED'`); s.String != "1" {
		t.Fatalf("ENDED events for the swapped sale = %s", s.String)
	}
	if s := one(`SELECT display_name FROM account_profiles WHERE account_id = 'ACC-SWAP'`); s.String != name {
		t.Fatalf("a swap archived the subscriber")
	}
	if s := one(`SELECT unassign_reason FROM account_device_mappings WHERE device_id = $1`, devS); s.String != "rma" {
		t.Fatalf("old mapping = %v", s)
	}
	saleS2, _ := inService("LC0005", "ACC-SWAP2")
	if r := e.call("mgr-a", "POST", "/api/v1/onboardings/"+saleS2+"/swap", map[string]any{"reason": "upgrade", "serial": "LC9999"}); r.code != 409 {
		t.Fatalf("swap to a serial with an open sale = %d %s", r.code, r.body)
	}
	if s := one(`SELECT state FROM onboardings WHERE id = $1`, saleS2); s.String != "IN_SERVICE" {
		t.Fatalf("a refused swap ended the sale: %s", s.String)
	}

	// A replacement serial that is another customer's device is refused,
	// and the sale stays in service.
	e.device("LCB001", &custB)
	if r := e.call("mgr-a", "POST", "/api/v1/onboardings/"+saleS2+"/swap", map[string]any{"reason": "rma", "serial": "LCB001"}); r.code != 409 {
		t.Fatalf("swap to another customer's device = %d %s", r.code, r.body)
	}
	if s := one(`SELECT state FROM onboardings WHERE id = $1`, saleS2); s.String != "IN_SERVICE" {
		t.Fatalf("a refused swap ended the sale: %s", s.String)
	}

	// A device reassigned to another customer since the sale is not the
	// sale's to release, move or reconfigure.
	saleX, devX := inService("LC0007", "ACC-MOVED")
	exec(`UPDATE devices SET customer_id = $2 WHERE id = $1`, devX, custB)
	for _, c := range []struct {
		method, path string
		body         map[string]any
	}{
		{"POST", "/end", map[string]any{"reason": "return", "factory_reset": true}},
		{"PUT", "/site", map[string]any{"site_id": siteA}},
		{"PUT", "/plan", map[string]any{"service_plan_id": planA}},
	} {
		if r := e.call("mgr-a", c.method, "/api/v1/onboardings/"+saleX+c.path, c.body); r.code != 409 {
			t.Fatalf("%s on a moved device = %d %s", c.path, r.code, r.body)
		}
	}
	if s := one(`SELECT customer_id::text = $2 FROM devices WHERE id = $1`, devX, custB); s.String != "true" {
		t.Fatalf("the moved device was released")
	}
	if s := one(`SELECT count(*)::text FROM jobs WHERE device_id = $1`, devX); s.String != "0" {
		t.Fatalf("a reset was queued on the moved device")
	}

	// --- Site and plan ------------------------------------------------------
	saleM, devM := inService("LC0006", "ACC-MOVE")
	siteA2, siteB := site(custA), site(custB)
	if r := e.call("mgr-a", "PUT", "/api/v1/onboardings/"+saleM+"/site", map[string]any{"site_id": siteB}); r.code != 400 {
		t.Fatalf("move to another customer's site = %d", r.code)
	}
	if r := e.call("mgr-a", "PUT", "/api/v1/onboardings/"+saleM+"/site", map[string]any{"site_id": siteA2}); r.code != 200 {
		t.Fatalf("move = %d %s", r.code, r.body)
	}
	if s := one(`SELECT (site_id::text = $2)::text FROM devices WHERE id = $1`, devM, siteA2); s.String != "true" {
		t.Fatalf("device site not moved")
	}
	quiet, busy, foreign := plan(custA, "QUIET", false), plan(custA, "BUSY", true), plan(custB, "OTHER", false)
	if r := e.call("mgr-a", "PUT", "/api/v1/onboardings/"+saleM+"/plan", map[string]any{"service_plan_id": foreign}); r.code != 400 {
		t.Fatalf("another customer's plan = %d", r.code)
	}
	if r := e.call("mgr-a", "PUT", "/api/v1/onboardings/"+saleM+"/plan", map[string]any{"service_plan_id": quiet}); r.code != 200 || !strings.Contains(r.body, `"state":"IN_SERVICE"`) {
		t.Fatalf("plan with nothing to write = %d %s", r.code, r.body)
	}
	// A sale the BSS made reads the plan code it now has; a console sale
	// has none to report.
	exec(`UPDATE onboardings SET bss_role = 'gateway', bss_service_plan = 'HOME' WHERE id = $1`, saleM)
	if r := e.call("mgr-a", "PUT", "/api/v1/onboardings/"+saleM+"/plan", map[string]any{"service_plan_id": quiet}); r.code != 200 {
		t.Fatalf("plan again = %d %s", r.code, r.body)
	}
	if s := one(`SELECT bss_service_plan FROM onboardings WHERE id = $1`, saleM); s.String != "QUIET" {
		t.Fatalf("bss_service_plan after a plan change = %q", s.String)
	}
	if s := one(`SELECT service_plan FROM account_device_mappings WHERE device_id = $1 AND unassigned_at IS NULL`, devM); s.String != "QUIET" {
		t.Fatalf("the account mapping kept the old plan: %q", s.String)
	}
	if s := one(`SELECT coalesce(bss_service_plan, 'none') FROM onboardings WHERE id = $1`, saleS2); s.String != "none" {
		t.Fatalf("a console sale gained a BSS plan code: %q", s.String)
	}
	if r := e.call("mgr-a", "PUT", "/api/v1/onboardings/"+saleM+"/plan", map[string]any{"service_plan_id": busy}); r.code != 200 || !strings.Contains(r.body, `"state":"DISCOVERED"`) {
		t.Fatalf("plan with configuration = %d %s", r.code, r.body)
	}
	if r := e.call("mgr-a", "PUT", "/api/v1/onboardings/"+saleM+"/site", map[string]any{"site_id": siteA}); r.code != 409 {
		t.Fatalf("move a sale that is reconfiguring = %d", r.code)
	}
}
