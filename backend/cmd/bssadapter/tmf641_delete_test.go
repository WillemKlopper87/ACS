package main

import (
	"net/http"
	"strings"
	"testing"

	"acs/internal/onboarding"
)

// TestTMF641DeleteNamingAnotherDeviceEndsNothing: a delete whose ouiSerial
// is not the device in the role fails and leaves the device mapped; with
// the right serial (in either id form) it ends it.
func TestTMF641DeleteNamingAnotherDeviceEndsNothing(t *testing.T) {
	ctx, h, db := preProvisionHandler(t)
	const deviceID = "44444444-4444-4444-4444-444444444444"
	seedOrderDevice(t, ctx, db, "acct-guard", deviceID)
	if _, err := db.ExecContext(ctx, `UPDATE devices SET oui = 'A4FC77', serial_number = 'GUARD01' WHERE id = $1`, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE account_device_mappings SET oui_serial = 'A4FC77+3TG02241+GUARD01' WHERE device_id = $1`, deviceID); err != nil {
		t.Fatal(err)
	}
	code, resp := postTMF641(t, h, tmf641OrderRequest{ExternalID: "SO-guard-1", AccountID: "acct-guard", OrderItem: []tmf641ItemRequest{
		{Action: "delete", Role: "gateway", OUISerial: "A4FC77+3TG02241+SOMEONEELSE"},
	}})
	if code != http.StatusCreated || resp["state"] != "failed" {
		t.Fatalf("delete naming another device = %d %v", code, resp)
	}
	if m, err := h.mappings.ActiveDeviceForAccount(ctx, "acct-guard", "gateway"); err != nil || m.DeviceID != deviceID {
		t.Fatalf("the device was ended anyway: %+v, %v", m, err)
	}
	code, resp = postTMF641(t, h, tmf641OrderRequest{ExternalID: "SO-guard-2", AccountID: "acct-guard", OrderItem: []tmf641ItemRequest{
		{Action: "delete", Role: "gateway", OUISerial: "A4FC77-GUARD01"},
	}})
	if code != http.StatusCreated || resp["state"] != "completed" {
		t.Fatalf("delete naming the device (Annex form) = %d %v", code, resp)
	}
}

// TestTMF641DeleteCancelsAnUncontactedSale: deleting the role of a sale
// whose CPE never connected completes and cancels the sale, so the order
// that added it fails instead of waiting for the TTL.
func TestTMF641DeleteCancelsAnUncontactedSale(t *testing.T) {
	ctx, h, _ := preProvisionHandler(t)
	if code, resp := postTMF641(t, h, tmf641OrderRequest{ExternalID: "SO-sale", AccountID: "acct-sale", OrderItem: []tmf641ItemRequest{
		{Action: "add", Role: "gateway", OUISerial: "A4FC77+3TG02241+NEVER01"},
	}}); code != http.StatusCreated {
		t.Fatalf("sale = %d %v", code, resp)
	}
	sale, err := h.onboarding.OpenBySerial(ctx, "NEVER01")
	if err != nil {
		t.Fatal(err)
	}
	code, resp := postTMF641(t, h, tmf641OrderRequest{ExternalID: "SO-unsell", AccountID: "acct-sale", OrderItem: []tmf641ItemRequest{
		{Action: "delete", Role: "gateway"},
	}})
	if code != http.StatusCreated || resp["state"] != "completed" {
		t.Fatalf("delete = %d %v", code, resp)
	}
	after, _ := h.onboarding.Get(ctx, sale.ID)
	if after.State != onboarding.StateCancelled || !strings.Contains(after.StateReason, "SO-unsell") {
		t.Fatalf("sale after the delete = %s %q", after.State, after.StateReason)
	}
	h.syncPreProvisioned(ctx)
	order, _ := h.mappings.FindServiceOrder(ctx, "SO-sale")
	items, _ := h.mappings.ServiceOrderItems(ctx, order.ID)
	if items[0].Status != "FAILED" || !strings.Contains(items[0].LastError, "SO-unsell") {
		t.Fatalf("the add behind the sale = %+v", items[0])
	}
}
