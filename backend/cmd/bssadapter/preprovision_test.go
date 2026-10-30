package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"acs/internal/bss"
	"acs/internal/onboarding"
)

func TestParseOUISerial(t *testing.T) {
	for in, want := range map[string][2]string{
		"A4FC77+3TG02241+CSC25430018F": {"A4FC77", "CSC25430018F"},
		"a4fc77-3TG02241-CSC25430018F": {"A4FC77", "CSC25430018F"},
		"001122-S2":                    {"001122", "S2"},
		"CSC25430018F":                 {"", "CSC25430018F"},
	} {
		if oui, serial := bss.ParseOUISerial(in); oui != want[0] || serial != want[1] {
			t.Errorf("ParseOUISerial(%q) = %q %q, want %q %q", in, oui, serial, want[0], want[1])
		}
	}
}

func preProvisionHandler(t *testing.T) (context.Context, *handler, *sql.DB) {
	t.Helper()
	ctx, h, db := newOrderTestHandler(t, acsRouterStub(t, nil, nil))
	h.onboarding = onboarding.NewRepository(db)
	return ctx, h, db
}

func postTMF641(t *testing.T, h *handler, req tmf641OrderRequest) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	h.createTMF641Order(rec, httptest.NewRequest(http.MethodPost, "/tmf-api/serviceOrdering/v4/serviceOrder", bytes.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestTMF641AddForAnUnseenCPEWaitsForIt is the pre-provisioning path end to
// end: the add records a sale and waits; the Wi-Fi modify behind it waits
// too; first contact maps the account PENDING_ACTIVE; IN_SERVICE makes it
// ACTIVE and completes the item, which releases the modify.
func TestTMF641AddForAnUnseenCPEWaitsForIt(t *testing.T) {
	ctx, h, db := preProvisionHandler(t)
	var custID string
	if err := db.QueryRowContext(ctx, `INSERT INTO customers (id, name) VALUES (gen_random_uuid(), 'Acme ISP') RETURNING id`).Scan(&custID); err != nil {
		t.Fatal(err)
	}
	plan, err := h.onboarding.CreatePlan(ctx, onboarding.ServicePlan{CustomerID: &custID, Code: "HOME-5G", Name: "Home 5G", AllowedPLMNs: []string{"65506"}})
	if err != nil {
		t.Fatal(err)
	}

	code, resp := postTMF641(t, h, tmf641OrderRequest{ExternalID: "SO-1", AccountID: "ACC-77", OrderItem: []tmf641ItemRequest{
		{Action: "add", Role: "gateway", OUISerial: "A4FC77+3TG02241+CSC25430018F", ServicePlan: "HOME-5G", IMSI: "655060000000013",
			Place: &tmf641Place{ID: "PREM-9", Name: "14 Oak Ave", City: "Embalenhle"}},
		{Action: "modify", Role: "gateway", Parameters: map[string]string{"ssid": "Oak"}},
	}})
	if code != http.StatusCreated {
		t.Fatalf("order = %d %v", code, resp)
	}
	if resp["state"] == "failed" {
		t.Fatalf("an unseen CPE failed the order: %v", resp)
	}
	order, _ := h.mappings.FindServiceOrder(ctx, "SO-1")
	items, _ := h.mappings.ServiceOrderItems(ctx, order.ID)
	if items[0].Status != bss.ItemAwaitingDevice || items[1].Status != "PENDING" {
		t.Fatalf("items = %s, %s; want AWAITING_DEVICE then PENDING", items[0].Status, items[1].Status)
	}

	o, err := h.onboarding.OpenBySerial(ctx, "CSC25430018F")
	if err != nil {
		t.Fatal(err)
	}
	if o.State != onboarding.StatePlanned || o.Source != "bss" || o.AccountRef != "ACC-77" || o.ExternalRef != "SO-1" ||
		o.ServicePlanID == nil || *o.ServicePlanID != plan.ID || o.CustomerID == nil || *o.CustomerID != custID || o.SiteID == nil || o.ExpectedOUI != "A4FC77" {
		t.Fatalf("onboarding = %+v", o)
	}
	if site, err := h.onboarding.SiteByRef(ctx, &custID, "PREM-9"); err != nil || site.ID != *o.SiteID || site.Name != "14 Oak Ave" {
		t.Fatalf("site = %+v, %v", site, err)
	}

	// A retried order (the reconciler resuming the item) gets the same sale.
	if again, err := h.preProvision(ctx, preProvisionRequest{AccountID: "ACC-77", OUISerial: "A4FC77+3TG02241+CSC25430018F"}); err != nil || again.ID != o.ID {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	if _, err := h.preProvision(ctx, preProvisionRequest{AccountID: "OTHER", OUISerial: "A4FC77+3TG02241+CSC25430018F"}); err == nil {
		t.Fatal("another account took over an open sale")
	}

	// Nothing to do before the CPE connects; the item says why.
	h.syncPreProvisioned(ctx)
	items, _ = h.mappings.ServiceOrderItems(ctx, order.ID)
	if items[0].Status != bss.ItemAwaitingDevice || !strings.Contains(items[0].LastError, "waiting for the device") {
		t.Fatalf("waiting item = %+v", items[0])
	}

	// First contact (as cmd/acs records it): the account is mapped, pending.
	var deviceID string
	if err := db.QueryRowContext(ctx, `INSERT INTO devices (id, oui_serial, oui, serial_number) VALUES (gen_random_uuid(), 'A4FC77+3TG02241+CSC25430018F', 'A4FC77', 'CSC25430018F') RETURNING id`).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.onboarding.Apply(ctx, *o, onboarding.Decision{State: onboarding.StateContacted, Reason: "waiting for the device to authenticate with its own credential"}, deviceID, nil); err != nil {
		t.Fatal(err)
	}
	h.syncPreProvisioned(ctx)
	m, err := h.mappings.ActiveDeviceForAccount(ctx, "ACC-77", "gateway")
	if err != nil || m.DeviceID != deviceID || m.Status != bss.StatusPendingActive || m.ServicePlan != "HOME-5G" {
		t.Fatalf("mapping on first contact = %+v, %v", m, err)
	}
	items, _ = h.mappings.ServiceOrderItems(ctx, order.ID)
	if items[0].Status != bss.ItemAwaitingDevice || !strings.Contains(items[0].LastError, "authenticate") {
		t.Fatalf("item after contact = %+v", items[0])
	}

	// In service: mapping ACTIVE, item COMPLETED, the modify is runnable.
	if _, err := db.ExecContext(ctx, `UPDATE onboardings SET state = 'IN_SERVICE', in_service_at = now() WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}
	h.syncPreProvisioned(ctx)
	if m, _ = h.mappings.ActiveDeviceForAccount(ctx, "ACC-77", "gateway"); m.Status != bss.StatusActive {
		t.Fatalf("mapping in service = %s, want ACTIVE", m.Status)
	}
	items, _ = h.mappings.ServiceOrderItems(ctx, order.ID)
	if items[0].Status != "COMPLETED" || items[0].MappingID != m.ID || items[1].Status != "PENDING" {
		t.Fatalf("items in service = %+v", items)
	}
	if _, err := db.ExecContext(ctx, `UPDATE service_orders SET created_at = now() - interval '1 minute' WHERE id = $1`, order.ID); err != nil {
		t.Fatal(err)
	}
	work, err := h.mappings.ClaimDueServiceOrderItems(ctx, 10)
	if err != nil || len(work) != 1 || work[0].Item.Seq != 1 {
		t.Fatalf("claimable after IN_SERVICE = %+v, %v; want the modify", work, err)
	}
}

// TestTMF641CancelWhileAwaitingDeviceCancelsTheSale: a sale cancelled
// before installation is cancellable over TMF641 and closes the onboarding.
func TestTMF641CancelWhileAwaitingDeviceCancelsTheSale(t *testing.T) {
	ctx, h, _ := preProvisionHandler(t)
	if code, resp := postTMF641(t, h, tmf641OrderRequest{ExternalID: "SO-2", AccountID: "ACC-8", OrderItem: []tmf641ItemRequest{
		{Action: "add", OUISerial: "001122-NEVERSEEN"}, {Action: "modify", Parameters: map[string]string{"ssid": "x"}},
	}}); code != http.StatusCreated {
		t.Fatalf("order = %d %v", code, resp)
	}
	order, _ := h.mappings.FindServiceOrder(ctx, "SO-2")
	if err := h.mappings.CancelServiceOrder(ctx, order.ID); err != nil {
		t.Fatalf("cancel while awaiting the device: %v", err)
	}
	h.syncPreProvisioned(ctx)
	items, _ := h.mappings.ServiceOrderItems(ctx, order.ID)
	if items[0].Status != "SKIPPED" || items[1].Status != "SKIPPED" {
		t.Fatalf("items = %+v, want both SKIPPED", items)
	}
	if _, err := h.onboarding.OpenBySerial(ctx, "NEVERSEEN"); err == nil {
		t.Fatal("the sale is still open after the order was cancelled")
	}
}

// TestMappingPreProvisionIsOptIn: the legacy mapping endpoint keeps its 404
// for an unknown device unless the caller asks to pre-provision.
func TestMappingPreProvisionIsOptIn(t *testing.T) {
	_, h, _ := preProvisionHandler(t)
	post := func(body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h.createMapping(rec, httptest.NewRequest(http.MethodPost, "/bss/v1/mappings", bytes.NewReader(b)))
		return rec
	}
	if rec := post(map[string]any{"account_id": "ACC-9", "oui_serial": "001122-UNSEEN"}); rec.Code != http.StatusNotFound {
		t.Fatalf("without pre_provision = %d %s, want 404", rec.Code, rec.Body)
	}
	rec := post(map[string]any{"account_id": "ACC-9", "oui_serial": "001122-UNSEEN", "pre_provision": true, "iccid": "89270000000000001234"})
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"AWAITING_DEVICE"`) {
		t.Fatalf("with pre_provision = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "8927") {
		t.Fatalf("response echoes the ICCID: %s", rec.Body)
	}
	if rec := post(map[string]any{"account_id": "ACC-9", "oui_serial": "001122-UNSEEN", "pre_provision": true, "role": "toaster"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad role = %d %s", rec.Code, rec.Body)
	}
}
