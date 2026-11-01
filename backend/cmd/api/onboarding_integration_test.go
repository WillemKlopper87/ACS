package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"acs/internal/operators"
	"acs/internal/tenancy"
)

func decodeBody[T any](t *testing.T, r resp) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(r.body), &v); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
	return v
}

// TestIntegration_OnboardingAPI covers a sale end to end at the API: site,
// plan and onboarding are created, SIM identifiers come back masked, a
// second open sale for the serial is refused, and a CPE that connected
// unsold is claimed from the unclaimed list.
func TestIntegration_OnboardingAPI(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", operators.RoleSuperAdmin)
	e.operator("viewer", operators.RoleReadOnly)

	site := e.call("root", "POST", "/api/v1/sites", map[string]any{"name": "14 Oak Ave", "city": "Embalenhle", "country": "ZA", "external_ref": "PREM-1"})
	if site.code != http.StatusCreated {
		t.Fatalf("create site: %d %s", site.code, site.body)
	}
	siteID := decodeBody[siteResponse](t, site).ID
	if r := e.call("root", "POST", "/api/v1/sites", map[string]any{"name": "dup", "external_ref": "PREM-1"}); r.code != http.StatusConflict {
		t.Fatalf("duplicate site reference: %d %s", r.code, r.body)
	}
	if r := e.call("root", "POST", "/api/v1/sites", map[string]any{"name": "half", "latitude": -26.1}); r.code != http.StatusBadRequest {
		t.Fatalf("latitude without longitude: %d %s", r.code, r.body)
	}

	if r := e.call("root", "POST", "/api/v1/service-plans", map[string]any{"code": "X", "name": "X", "allowed_plmns": []string{"MTN"}}); r.code != http.StatusBadRequest || !strings.Contains(r.body, "MCC+MNC") {
		t.Fatalf("bad PLMN: %d %s", r.code, r.body)
	}
	plan := e.call("root", "POST", "/api/v1/service-plans", map[string]any{"code": "HOME-5G", "name": "Home 5G",
		"allowed_plmns": []string{"65506"}, "cellular": map[string]any{"apn": "internet", "dnn": "internet"}})
	if plan.code != http.StatusCreated {
		t.Fatalf("create plan: %d %s", plan.code, plan.body)
	}
	p := decodeBody[planResponse](t, plan)
	if !p.RequireDiscovery {
		t.Fatal("a plan with a cellular profile must require discovery")
	}
	if r := e.call("root", "POST", "/api/v1/service-plans", map[string]any{"code": "HOME-5G", "name": "again"}); r.code != http.StatusConflict {
		t.Fatalf("duplicate plan code: %d %s", r.code, r.body)
	}

	sale := map[string]any{"expected_serial": "CSC25430018F", "expected_iccid": "89270000000000001234", "expected_imsi": "655060000000013",
		"account_ref": "ACC-77", "site_id": siteID, "service_plan_id": p.ID, "commercial_model": "retail"}
	created := e.call("root", "POST", "/api/v1/onboardings", sale)
	if created.code != http.StatusCreated {
		t.Fatalf("create onboarding: %d %s", created.code, created.body)
	}
	if strings.Contains(created.body, "89270000000000001234") || strings.Contains(created.body, "655060000000013") {
		t.Fatalf("onboarding response exposes a full SIM identifier: %s", created.body)
	}
	o := decodeBody[onboardingResponse](t, created)
	if o.State != "PLANNED" || o.Source != "console" || o.RequestedBy == "" || !strings.HasPrefix(o.ExpectedIMSI, "65506") {
		t.Fatalf("created = %+v", o)
	}
	if r := e.call("root", "POST", "/api/v1/onboardings", sale); r.code != http.StatusConflict {
		t.Fatalf("second open sale for the serial: %d %s", r.code, r.body)
	}
	if r := e.call("root", "POST", "/api/v1/onboardings", map[string]any{"expected_serial": "Z", "role": "router"}); r.code != http.StatusBadRequest {
		t.Fatalf("unknown role: %d %s", r.code, r.body)
	}
	var audit string
	if err := e.db.QueryRowContext(e.ctx, `SELECT details::text FROM audit_log WHERE action = 'OnboardingCreated'`).Scan(&audit); err != nil || strings.Contains(audit, "8927") {
		t.Fatalf("audit = %q, %v; want no SIM identifiers", audit, err)
	}

	// Read-only can look, not change.
	if r := e.call("root", "GET", "/api/v1/onboardings?state=PLANNED", nil); r.code != http.StatusOK || !strings.Contains(r.body, o.ID) {
		t.Fatalf("list by state: %d %s", r.code, r.body)
	}
	if r := e.call("viewer", "GET", "/api/v1/onboardings", nil); r.code != http.StatusOK {
		t.Fatalf("viewer list: %d %s", r.code, r.body)
	}
	if r := e.call("viewer", "POST", "/api/v1/onboardings/"+o.ID+"/cancel", nil); r.code != http.StatusForbidden {
		t.Fatalf("viewer cancel: %d", r.code)
	}

	// A CPE that connected unsold is listed, then claimed.
	stray := e.device("STRAY0001", nil)
	unclaimed := e.call("root", "GET", "/api/v1/onboardings/unclaimed", nil)
	if unclaimed.code != http.StatusOK || !strings.Contains(unclaimed.body, stray) {
		t.Fatalf("unclaimed: %d %s", unclaimed.code, unclaimed.body)
	}
	claim := e.call("root", "POST", "/api/v1/onboardings", map[string]any{"device_id": stray, "site_id": siteID})
	if claim.code != http.StatusCreated {
		t.Fatalf("claim: %d %s", claim.code, claim.body)
	}
	if c := decodeBody[onboardingResponse](t, claim); c.ExpectedSerial != "STRAY0001" || c.DeviceID == nil || *c.DeviceID != stray {
		t.Fatalf("claimed = %+v", c)
	}
	if r := e.call("root", "GET", "/api/v1/onboardings/unclaimed", nil); strings.Contains(r.body, stray) {
		t.Fatalf("claimed device still unclaimed: %s", r.body)
	}
	if r := decodeBody[struct{ Items []onboardingResponse }](t, e.call("root", "GET", "/api/v1/onboardings?device_id="+stray, nil)); len(r.Items) != 1 || r.Items[0].ExpectedSerial != "STRAY0001" {
		t.Fatalf("onboardings of the device = %+v", r.Items)
	}

	// Cancel once; a closed onboarding cannot be cancelled or retried.
	if r := e.call("root", "POST", "/api/v1/onboardings/"+o.ID+"/cancel", nil); r.code != http.StatusOK || !strings.Contains(r.body, "CANCELLED") {
		t.Fatalf("cancel: %d %s", r.code, r.body)
	}
	for _, action := range []string{"cancel", "retry"} {
		if r := e.call("root", "POST", "/api/v1/onboardings/"+o.ID+"/"+action, nil); r.code != http.StatusConflict {
			t.Fatalf("%s a cancelled onboarding: %d %s", action, r.code, r.body)
		}
	}
}

// TestIntegration_OnboardingTenantScope: a scoped manager works only on
// their own customer's sites and sales, sees the shared plan catalog, and
// sees no unclaimed (tenantless) CPEs.
func TestIntegration_OnboardingTenantScope(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", operators.RoleSuperAdmin)
	custA, custB := e.customer("A"), e.customer("B")
	e.operator("mgr", operators.RoleManager, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})

	if r := e.call("mgr", "POST", "/api/v1/sites", map[string]any{"name": "no tenant"}); r.code != http.StatusBadRequest {
		t.Fatalf("scoped site without customer: %d %s", r.code, r.body)
	}
	if r := e.call("mgr", "POST", "/api/v1/sites", map[string]any{"name": "theirs", "customer_id": custB}); r.code != http.StatusBadRequest {
		t.Fatalf("scoped site for another customer: %d %s", r.code, r.body)
	}
	own := e.call("mgr", "POST", "/api/v1/sites", map[string]any{"name": "ours", "customer_id": custA})
	if own.code != http.StatusCreated {
		t.Fatalf("scoped site: %d %s", own.code, own.body)
	}
	otherSite := decodeBody[siteResponse](t, e.call("root", "POST", "/api/v1/sites", map[string]any{"name": "B site", "customer_id": custB}))
	if r := e.call("mgr", "GET", "/api/v1/sites/"+otherSite.ID, nil); r.code != http.StatusNotFound {
		t.Fatalf("other tenant's site: %d", r.code)
	}
	if r := e.call("mgr", "GET", "/api/v1/sites", nil); strings.Contains(r.body, otherSite.ID) {
		t.Fatalf("site list leaks another tenant's address: %s", r.body)
	}

	shared := decodeBody[planResponse](t, e.call("root", "POST", "/api/v1/service-plans", map[string]any{"code": "SHARED", "name": "Shared"}))
	if r := e.call("mgr", "GET", "/api/v1/service-plans/"+shared.ID, nil); r.code != http.StatusOK {
		t.Fatalf("shared plan: %d", r.code)
	}

	if r := e.call("mgr", "POST", "/api/v1/onboardings", map[string]any{"expected_serial": "S1", "customer_id": custA, "site_id": otherSite.ID}); r.code != http.StatusBadRequest {
		t.Fatalf("sale on another tenant's site: %d %s", r.code, r.body)
	}
	mine := e.call("mgr", "POST", "/api/v1/onboardings", map[string]any{"expected_serial": "S1", "customer_id": custA, "service_plan_id": shared.ID})
	if mine.code != http.StatusCreated {
		t.Fatalf("scoped sale: %d %s", mine.code, mine.body)
	}
	theirs := decodeBody[onboardingResponse](t, e.call("root", "POST", "/api/v1/onboardings", map[string]any{"expected_serial": "S2", "customer_id": custB}))
	if r := e.call("mgr", "GET", "/api/v1/onboardings/"+theirs.ID, nil); r.code != http.StatusNotFound {
		t.Fatalf("other tenant's onboarding: %d", r.code)
	}
	if r := e.call("mgr", "GET", "/api/v1/onboardings", nil); strings.Contains(r.body, theirs.ID) || !strings.Contains(r.body, "S1") {
		t.Fatalf("scoped list: %s", r.body)
	}
	e.device("STRAY0002", nil)
	if r := e.call("mgr", "GET", "/api/v1/onboardings/unclaimed", nil); r.code != http.StatusOK || strings.Contains(r.body, "STRAY0002") {
		t.Fatalf("scoped unclaimed: %d %s", r.code, r.body)
	}

	// A sale may not take another tenant's device by naming its serial.
	owned := e.device("OWNEDB01", &custB)
	if r := e.call("mgr", "POST", "/api/v1/onboardings", map[string]any{"expected_serial": "OWNEDB01", "customer_id": custA}); r.code != http.StatusConflict {
		t.Fatalf("scoped sale of another tenant's serial: %d %s", r.code, r.body)
	}
	if r := e.call("root", "POST", "/api/v1/onboardings", map[string]any{"expected_serial": "OWNEDB01", "customer_id": custA}); r.code != http.StatusConflict {
		t.Fatalf("sale moving a device across tenants: %d %s", r.code, r.body)
	}
	if r := e.call("root", "POST", "/api/v1/onboardings", map[string]any{"device_id": owned, "customer_id": custA}); r.code != http.StatusConflict {
		t.Fatalf("claim moving a device across tenants: %d %s", r.code, r.body)
	}
	if r := e.call("root", "POST", "/api/v1/onboardings", map[string]any{"expected_serial": "OWNEDB01", "customer_id": custB}); r.code != http.StatusCreated {
		t.Fatalf("sale for the owning tenant: %d %s", r.code, r.body)
	}
}
