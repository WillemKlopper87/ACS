package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"acs/internal/onboarding"
)

func (e *bindingEnv) postMappingWithKey(t *testing.T, token, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/bss/v1/mappings", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	e.h.createMapping(rec, req)
	return rec
}

// TestMappingIdempotencyKey: a retried mapping with the same key answers
// as the first time (a plain retry would get 409 role already assigned); the
// key with another body is refused; keys are per account, so another
// customer's identical key is its own.
func TestMappingIdempotencyKey(t *testing.T) {
	e := newBindingEnv(t)
	tokA, tokB := bindingToken(t, e.custA, false), bindingToken(t, e.custB, false)
	body := map[string]any{"account_id": "A.1", "oui_serial": "001122-DEV-A", "service_plan": "HOME"}
	first := e.postMappingWithKey(t, tokA, "k-1", body)
	if first.Code != http.StatusOK {
		t.Fatalf("first = %d %s", first.Code, first.Body)
	}
	again := e.postMappingWithKey(t, tokA, "k-1", body)
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" ||
		!bytes.Equal(bytes.TrimSpace(again.Body.Bytes()), bytes.TrimSpace(first.Body.Bytes())) {
		t.Fatalf("replay = %d %s, first %s", again.Code, again.Body, first.Body)
	}
	if plain := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA, body); plain.Code != http.StatusConflict {
		t.Fatalf("a retry without the key = %d, want 409 (that is why the key exists)", plain.Code)
	}
	body["service_plan"] = "OTHER"
	if other := e.postMappingWithKey(t, tokA, "k-1", body); other.Code != http.StatusConflict || !bytes.Contains(other.Body.Bytes(), []byte("ErrIdempotencyKeyReused")) {
		t.Fatalf("same key, other body = %d %s", other.Code, other.Body)
	}

	sale := map[string]any{"account_id": "B.1", "oui_serial": "001122+X+NEWCPE", "pre_provision": true}
	accepted := e.postMappingWithKey(t, tokB, "k-1", sale)
	replayed := e.postMappingWithKey(t, tokB, "k-1", sale)
	if accepted.Code != http.StatusAccepted || replayed.Code != http.StatusAccepted || replayed.Header().Get("Location") == "" ||
		replayed.Header().Get("Location") != accepted.Header().Get("Location") {
		t.Fatalf("pre_provision with a key = %d %s / %d %v", accepted.Code, accepted.Body, replayed.Code, replayed.Header())
	}
}

// TestTMF641ExternalIDReuseWithAnotherOrder: externalId is the TMF641
// idempotency key; a repeat with the same order replays it, another order
// under it is refused rather than answered with the first order.
func TestTMF641ExternalIDReuseWithAnotherOrder(t *testing.T) {
	_, h, _ := preProvisionHandler(t)
	order := tmf641OrderRequest{ExternalID: "SO-K", AccountID: "ACC-K", OrderItem: []tmf641ItemRequest{{Action: "add", Role: "gateway", OUISerial: "A4FC77+3TG02241+K01"}}}
	if code, resp := postTMF641(t, h, order); code != http.StatusCreated {
		t.Fatalf("first = %d %v", code, resp)
	}
	if code, resp := postTMF641(t, h, order); code != http.StatusOK || resp["externalId"] != "SO-K" {
		t.Fatalf("repeat = %d %v", code, resp)
	}
	order.OrderItem[0].OUISerial = "A4FC77+3TG02241+K02"
	if code, resp := postTMF641(t, h, order); code != http.StatusConflict || resp["code"] != "ErrIdempotencyKeyReused" {
		t.Fatalf("another order under the same externalId = %d %v", code, resp)
	}
}

// TestBSSRetriesAndCancelsItsSale: the account's owner retries a held sale
// and cancels it; another customer cannot; a state that does not allow it
// is a 409.
func TestBSSRetriesAndCancelsItsSale(t *testing.T) {
	e := newBindingEnv(t)
	tokA, tokB := bindingToken(t, e.custA, false), bindingToken(t, e.custB, false)
	rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA,
		map[string]any{"account_id": "A.7", "oui_serial": "001122+X+HELD01", "pre_provision": true})
	var created struct {
		OnboardingID string `json:"onboarding_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); rec.Code != http.StatusAccepted || err != nil {
		t.Fatalf("sale = %d %s", rec.Code, rec.Body)
	}
	if _, err := e.db.ExecContext(e.ctx, `UPDATE onboardings SET state = 'ON_HOLD', state_code = 'SIM_MISMATCH', state_reason = 'held' WHERE id = $1`, created.OnboardingID); err != nil {
		t.Fatal(err)
	}
	post := func(fn http.HandlerFunc, token string) (int, saleResponse) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.SetPathValue("id", created.OnboardingID)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		fn(rec, req)
		var s saleResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &s)
		return rec.Code, s
	}
	if code, _ := post(e.h.retrySale, tokB); code != http.StatusNotFound {
		t.Fatalf("another customer retrying = %d", code)
	}
	if code, s := post(e.h.retrySale, tokA); code != http.StatusOK || s.State != onboarding.StatePlanned || s.ReasonCode != "" {
		t.Fatalf("retry = %d %+v", code, s)
	}
	if code, _ := post(e.h.retrySale, tokA); code != http.StatusConflict {
		t.Fatalf("retrying a PLANNED sale = %d, want 409", code)
	}
	if code, s := post(e.h.cancelSale, tokA); code != http.StatusOK || s.State != onboarding.StateCancelled {
		t.Fatalf("cancel = %d %+v", code, s)
	}
	var events int
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM tmf_events WHERE event_type = 'OnboardingStateChangeEvent' AND customer_id = $1`, e.custA).Scan(&events); err != nil || events != 2 {
		t.Fatalf("state change events = %d, %v; want retry and cancel", events, err)
	}
}

// TestCustomerDevicesArePaged: a customer reads its own devices in pages
// with an opaque cursor; another customer's are not in them.
func TestCustomerDevicesArePaged(t *testing.T) {
	e := newBindingEnv(t)
	for _, serial := range []string{"P1", "P2", "P3"} {
		if _, err := e.db.ExecContext(e.ctx, `INSERT INTO devices (id, oui_serial, customer_id) VALUES (gen_random_uuid(), $1, $2)`, "001122-"+serial, e.custA); err != nil {
			t.Fatal(err)
		}
	}
	e.contact(t, "DEV-B", e.custB)
	tokA := bindingToken(t, e.custA, false)
	page := func(query string) customerDevicesResponse {
		rec := e.call(t, e.h.listCustomerDevices, http.MethodGet, "/bss/v1/devices"+query, tokA, nil)
		var out customerDevicesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("devices%s = %d %s", query, rec.Code, rec.Body)
		}
		return out
	}
	first := page("?limit=2")
	if len(first.Items) != 2 || first.NextCursor == "" || first.Items[0].OUISerial != "001122-P1" {
		t.Fatalf("first page = %+v", first)
	}
	second := page("?limit=2&cursor=" + first.NextCursor)
	if len(second.Items) != 1 || second.NextCursor != "" || second.Items[0].OUISerial != "001122-P3" {
		t.Fatalf("second page = %+v", second)
	}
	if rec := e.call(t, e.h.listCustomerDevices, http.MethodGet, "/bss/v1/devices?customer_id="+e.custB, tokA, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("naming another customer = %d", rec.Code)
	}
}
