package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTMF641ReadAndCancelByIDOrExternalID: an order's href (its own id)
// and its externalId both address it for GET and the cancel PATCH. Before,
// GET answered only the externalId (so the href was a 404) and cancel
// never worked.
func TestTMF641ReadAndCancelByIDOrExternalID(t *testing.T) {
	_, h, _ := preProvisionHandler(t)
	place := func(ext, serial string) map[string]any {
		t.Helper()
		code, resp := postTMF641(t, h, tmf641OrderRequest{ExternalID: ext, AccountID: "acct-read", OrderItem: []tmf641ItemRequest{
			{Action: "add", Role: "gateway", OUISerial: "A4FC77+3TG02241+" + serial},
		}})
		if code != http.StatusCreated || resp["state"] != "acknowledged" {
			t.Fatalf("order %s = %d %v", ext, code, resp)
		}
		return resp
	}
	call := func(fn http.HandlerFunc, method, id string, body any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "/tmf-api/serviceOrdering/v4/serviceOrder/"+id, bytes.NewReader(raw))
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		fn(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	first := place("SO-read-1", "READ01")
	id := first["id"].(string)
	if first["href"] != "/tmf-api/serviceOrdering/v4/serviceOrder/"+id {
		t.Fatalf("href = %v", first["href"])
	}
	for _, ref := range []string{id, "SO-read-1"} {
		if code, got := call(h.getTMF641Order, http.MethodGet, ref, nil); code != http.StatusOK || got["id"] != id || got["externalId"] != "SO-read-1" {
			t.Fatalf("GET %s = %d %v", ref, code, got)
		}
	}
	if code, _ := call(h.getTMF641Order, http.MethodGet, "SO-missing", nil); code != http.StatusNotFound {
		t.Fatalf("GET unknown = %d", code)
	}

	// Cancel one order by its externalId and another by its id.
	second := place("SO-read-2", "READ02")
	for _, ref := range []string{"SO-read-1", second["id"].(string)} {
		code, got := call(h.cancelTMF641Order, http.MethodPatch, ref, map[string]string{"state": "cancelled"})
		if code != http.StatusOK || got["state"] != "cancelled" {
			t.Fatalf("cancel %s = %d %v", ref, code, got)
		}
	}
	if code, _ := call(h.cancelTMF641Order, http.MethodPatch, "SO-missing", map[string]string{"state": "cancelled"}); code != http.StatusNotFound {
		t.Fatalf("cancel unknown = %d", code)
	}
}
