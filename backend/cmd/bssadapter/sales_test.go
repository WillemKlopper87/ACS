package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"acs/internal/onboarding"
)

func getSaleFor(t *testing.T, h *handler, id, token string) (int, saleResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/bss/v1/onboardings/"+id, nil)
	req.SetPathValue("id", id)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.getSale(rec, req)
	var out saleResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestSaleReadAndUnknownPlan: a pre-provisioned sale with a plan code the
// ACS does not know is accepted with a PLAN_NOT_FOUND warning, points to
// its sale read, and the read shows where it is and when it expires.
func TestSaleReadAndUnknownPlan(t *testing.T) {
	ctx, h, _ := preProvisionHandler(t)
	h.saleTTL = time.Hour
	body := `{"account_id":"ACC-1","oui_serial":"A4FC77+3TG02241+SALE01","service_plan":"NO-SUCH-PLAN","pre_provision":true}`
	rec := httptest.NewRecorder()
	h.createMapping(rec, httptest.NewRequest(http.MethodPost, "/bss/v1/mappings", strings.NewReader(body)))
	var resp struct {
		OnboardingID string        `json:"onboarding_id"`
		Href         string        `json:"href"`
		PlanMatched  bool          `json:"plan_matched"`
		Warnings     []saleWarning `json:"warnings"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != http.StatusAccepted || resp.PlanMatched || len(resp.Warnings) != 1 || resp.Warnings[0].Code != warningPlanNotFound ||
		resp.Href != "/bss/v1/onboardings/"+resp.OnboardingID || rec.Header().Get("Location") != resp.Href {
		t.Fatalf("pre-provision = %d %s", rec.Code, rec.Body)
	}

	code, sale := getSaleFor(t, h, resp.OnboardingID, "")
	if code != http.StatusOK || sale.State != onboarding.StatePlanned || sale.AccountID != "ACC-1" || sale.SerialNumber != "SALE01" ||
		sale.ServicePlan != "NO-SUCH-PLAN" || sale.PlanMatched || len(sale.Warnings) != 1 || sale.ExpiresAt == nil ||
		sale.ExpiresAt.Location() != time.UTC || !sale.ExpiresAt.After(time.Now()) {
		t.Fatalf("sale = %d %+v", code, sale)
	}
	if code, _ := getSaleFor(t, h, "00000000-0000-0000-0000-000000000000", ""); code != http.StatusNotFound {
		t.Fatalf("unknown sale = %d", code)
	}
	_ = ctx
}

// TestSaleReadIsCustomerScoped: another customer's bound client cannot read
// a sale, and an anonymous caller is refused before learning anything.
func TestSaleReadIsCustomerScoped(t *testing.T) {
	e := newBindingEnv(t)
	tokA, tokB := bindingToken(t, e.custA, false), bindingToken(t, e.custB, false)
	rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA,
		map[string]any{"account_id": "A.9", "oui_serial": "001122+X+NEWCPE", "pre_provision": true})
	var resp struct {
		OnboardingID string `json:"onboarding_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); rec.Code != http.StatusAccepted || err != nil {
		t.Fatalf("pre-provision = %d %s", rec.Code, rec.Body)
	}
	if code, sale := getSaleFor(t, e.h, resp.OnboardingID, tokA); code != http.StatusOK || sale.AccountID != "A.9" {
		t.Fatalf("owner reading its sale = %d %+v", code, sale)
	}
	if code, _ := getSaleFor(t, e.h, resp.OnboardingID, tokB); code != http.StatusNotFound {
		t.Fatalf("another customer reading the sale = %d", code)
	}
	for _, id := range []string{resp.OnboardingID, "00000000-0000-0000-0000-000000000000"} {
		if code, _ := getSaleFor(t, e.h, id, ""); code != http.StatusUnauthorized {
			t.Fatalf("anonymous read of %s = %d", id, code)
		}
	}
}

// TestSaleExpiresWithoutDeviceContact: a TMF641 add whose CPE never
// connects is cancelled after the sale TTL (TTL_EXPIRED, with an event),
// its item FAILED and the order behind it skipped; the serial is free for
// a new sale.
func TestSaleExpiresWithoutDeviceContact(t *testing.T) {
	ctx, h, db := preProvisionHandler(t)
	h.saleTTL = time.Hour
	code, resp := postTMF641(t, h, tmf641OrderRequest{ExternalID: "SO-TTL", AccountID: "ACC-TTL", OrderItem: []tmf641ItemRequest{
		{Action: "add", Role: "gateway", OUISerial: "A4FC77+3TG02241+TTL01"},
		{Action: "modify", Role: "gateway", Parameters: map[string]string{"ssid": "Late"}},
	}})
	if code != http.StatusCreated {
		t.Fatalf("order = %d %v", code, resp)
	}
	o, err := h.onboarding.OpenBySerial(ctx, "TTL01")
	if err != nil {
		t.Fatal(err)
	}

	h.syncPreProvisioned(ctx)
	if again, _ := h.onboarding.Get(ctx, o.ID); again.State != onboarding.StatePlanned {
		t.Fatalf("a fresh sale expired: %s", again.State)
	}
	if _, err := db.ExecContext(ctx, `UPDATE onboardings SET created_at = now() - interval '2 hours' WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}
	h.syncPreProvisioned(ctx)

	expired, _ := h.onboarding.Get(ctx, o.ID)
	if expired.State != onboarding.StateCancelled || expired.StateCode != onboarding.CodeTTLExpired {
		t.Fatalf("expired sale = %s %s", expired.State, expired.StateCode)
	}
	order, _ := h.mappings.FindServiceOrder(ctx, "SO-TTL")
	items, _ := h.mappings.ServiceOrderItems(ctx, order.ID)
	if items[0].Status != "FAILED" || !strings.Contains(items[0].LastError, "no device contact") || items[1].Status != "SKIPPED" {
		t.Fatalf("items after expiry = %+v", items)
	}
	var payload string
	if err := db.QueryRowContext(ctx, `SELECT payload::text FROM tmf_events WHERE event_type = 'OnboardingStateChangeEvent' AND account_id = 'ACC-TTL'`).Scan(&payload); err != nil ||
		!strings.Contains(payload, onboarding.CodeTTLExpired) {
		t.Fatalf("expiry event = %s, %v", payload, err)
	}
	if _, err := h.preProvision(ctx, preProvisionRequest{AccountID: "ACC-OTHER", OUISerial: "A4FC77+3TG02241+TTL01"}); err != nil {
		t.Fatalf("the serial is not free after expiry: %v", err)
	}
}
