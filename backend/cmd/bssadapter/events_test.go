package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"acs/internal/bss"
)

type eventsResp struct {
	Events         []bss.EventEnvelope `json:"events"`
	NextAfter      int64               `json:"next_after"`
	LatestSequence int64               `json:"latest_sequence"`
	Gap            bool                `json:"gap"`
}

func (e *bindingEnv) contact(t *testing.T, serial, customer string) {
	t.Helper()
	if _, err := e.db.ExecContext(e.ctx, `UPDATE devices SET customer_id = $2::uuid, last_inform_at = now(), online_status = 'ONLINE'
		WHERE oui_serial = $1`, "001122-"+serial, customer); err != nil {
		t.Fatal(err)
	}
}

func (e *bindingEnv) replay(t *testing.T, token, query string) (int, eventsResp) {
	t.Helper()
	rec := e.call(t, e.h.listEvents, http.MethodGet, "/bss/v1/events"+query, token, nil)
	var out eventsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestEventReplayIsPerCustomer: device events land in their customer's
// stream with increasing sequence numbers; a customer reads only its own,
// pages with next_after, and is told when retention removed events.
func TestEventReplayIsPerCustomer(t *testing.T) {
	e := newBindingEnv(t)
	tokA, tokB := bindingToken(t, e.custA, false), bindingToken(t, e.custB, false)
	e.contact(t, "DEV-A", e.custA)
	e.contact(t, "DEV-B", e.custB)
	if _, err := e.db.ExecContext(e.ctx, `UPDATE devices SET online_status = 'UNREACHABLE' WHERE oui_serial = '001122-DEV-A'`); err != nil {
		t.Fatal(err)
	}

	code, page := e.replay(t, tokA, "?after=0")
	if code != http.StatusOK || len(page.Events) != 2 || page.Events[0].Type != "DeviceSeen" || page.Events[1].Type != "DeviceUnreachable" {
		t.Fatalf("A's stream = %d %+v", code, page)
	}
	first := page.Events[0]
	if first.Device == nil || first.Device.OUISerial != "001122-DEV-A" || first.CustomerID == nil || *first.CustomerID != e.custA ||
		page.Events[1].Sequence <= first.Sequence || page.NextAfter != page.Events[1].Sequence || page.Gap {
		t.Fatalf("envelope = %+v, page = %+v", first, page)
	}
	if _, page = e.replay(t, tokA, "?after=1"); len(page.Events) != 1 {
		t.Fatalf("after the first event = %+v", page)
	}
	if _, page = e.replay(t, tokB, "?after=0"); len(page.Events) != 1 || page.Events[0].Device.OUISerial != "001122-DEV-B" {
		t.Fatalf("B's stream = %+v", page)
	}
	if code, _ := e.replay(t, tokB, "?customer_id="+e.custA); code != http.StatusNotFound {
		t.Fatalf("B naming A's stream = %d", code)
	}
	if code, page := e.replay(t, bindingToken(t, "", true), "?customer_id="+e.custA); code != http.StatusOK || len(page.Events) != 2 {
		t.Fatalf("global client reading A = %d %+v", code, page)
	}
	listed := scopedToken(t, "", bss.ScopeTMFRead)
	if code, _ := e.replay(t, listed, ""); code != http.StatusForbidden {
		t.Fatalf("account-listed client = %d", code)
	}

	if _, err := e.db.ExecContext(e.ctx, `UPDATE tmf_event_sequences SET purged_through = 1 WHERE scope = $1`, e.custA); err != nil {
		t.Fatal(err)
	}
	if _, page = e.replay(t, tokA, "?after=0"); !page.Gap {
		t.Fatal("a cursor below the retention watermark is not reported as a gap")
	}
	if _, page = e.replay(t, tokA, "?after=1"); page.Gap {
		t.Fatal("a cursor at the watermark is reported as a gap")
	}
}

// TestWebhookEnvelopeAndDeliveryOperations: a subscribed event is delivered
// in the envelope (with the older TMF688 keys kept); a failed attempt says
// why; a redeliver starts again; rotation signs with both secrets.
func TestWebhookEnvelopeAndDeliveryOperations(t *testing.T) {
	e := newBindingEnv(t)
	tokA := bindingToken(t, e.custA, false)
	e.h.webhooks = bss.NewWebhookRepository(e.db)
	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA, map[string]any{"account_id": "A.1", "oui_serial": "001122-DEV-A"}); rec.Code != http.StatusOK {
		t.Fatalf("mapping = %d %s", rec.Code, rec.Body)
	}
	acct := "A.1"
	sub, err := e.h.webhooks.CreateSubscription(e.ctx, &acct, "http://127.0.0.1:9/hook", "old-secret-0123456789", []string{"DeviceSeen"})
	if err != nil || sub.CreatedAt.Location() != time.UTC {
		t.Fatalf("subscription = %+v, %v; want created_at in UTC", sub, err)
	}
	e.contact(t, "DEV-A", e.custA)
	if n, err := e.h.webhooks.DispatchTMFEvents(e.ctx, time.Now().Add(-time.Hour), 100); err != nil || n != 1 {
		t.Fatalf("dispatched = %d, %v", n, err)
	}
	var payload map[string]any
	var raw []byte
	if err := e.db.QueryRowContext(e.ctx, `SELECT payload FROM webhook_deliveries WHERE subscription_id = $1`, sub.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &payload)
	device, _ := payload["device"].(map[string]any)
	if payload["type"] != "DeviceSeen" || payload["sequence"] == nil || payload["customer_id"] != e.custA || payload["account_id"] != "A.1" ||
		device["oui_serial"] != "001122-DEV-A" || payload["data"] == nil || payload["eventType"] != "DeviceSeen" {
		t.Fatalf("delivered payload = %s", raw)
	}

	// Loopback is refused by the network policy: the attempt fails, with why.
	e.h.deliverDueWebhooks(e.ctx, http.DefaultClient)
	req := httptest.NewRequest(http.MethodGet, "/bss/v1/webhooks/"+sub.ID+"/deliveries?status=PENDING", nil)
	req.SetPathValue("id", sub.ID)
	req.Header.Set("Authorization", "Bearer "+tokA)
	rec := httptest.NewRecorder()
	e.h.listWebhookDeliveries(rec, req)
	var list struct{ Items []bss.DeliveryStatus }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list.Items) != 1 || list.Items[0].Attempts != 1 || !strings.Contains(list.Items[0].LastError, "network policy") {
		t.Fatalf("deliveries = %d %s", rec.Code, rec.Body)
	}
	if list.Items[0].EventID == "" || list.Items[0].EventSequence == nil {
		t.Fatalf("delivery does not name its event: %s", rec.Body)
	}
	if !strings.HasSuffix(payload["occurred_at"].(string), "Z") {
		t.Fatalf("webhook occurred_at = %v, want UTC", payload["occurred_at"])
	}
	tokB := bindingToken(t, e.custB, false)
	req.Header.Set("Authorization", "Bearer "+tokB)
	rec = httptest.NewRecorder()
	e.h.listWebhookDeliveries(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("another customer reading the deliveries = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/redeliver", nil)
	req.SetPathValue("id", sub.ID)
	req.SetPathValue("delivery_id", list.Items[0].ID)
	req.Header.Set("Authorization", "Bearer "+tokA)
	rec = httptest.NewRecorder()
	e.h.redeliverWebhook(rec, req)
	var attempts int
	if err := e.db.QueryRowContext(e.ctx, `SELECT attempts FROM webhook_deliveries WHERE id = $1`, list.Items[0].ID).Scan(&attempts); rec.Code != http.StatusAccepted || err != nil || attempts != 0 {
		t.Fatalf("redeliver = %d, attempts %d, %v", rec.Code, attempts, err)
	}

	// Rotation: until the overlap ends both secrets sign.
	req = httptest.NewRequest(http.MethodPost, "/rotate", strings.NewReader(`{"overlap_seconds":3600}`))
	req.ContentLength = int64(len(`{"overlap_seconds":3600}`))
	req.SetPathValue("id", sub.ID)
	req.Header.Set("Authorization", "Bearer "+tokA)
	rec = httptest.NewRecorder()
	e.h.rotateWebhookSecret(rec, req)
	var rotated struct {
		Secret     string
		ValidUntil string `json:"previous_secret_valid_until"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rotated); rec.Code != http.StatusOK || err != nil || rotated.Secret == "" || !strings.HasSuffix(rotated.ValidUntil, "Z") {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body)
	}
	due, err := e.h.webhooks.DueDeliveries(e.ctx, 10)
	if err != nil || len(due) != 1 || due[0].PreviousSecret != "old-secret-0123456789" || due[0].Secret != rotated.Secret {
		t.Fatalf("due delivery secrets = %+v, %v", due, err)
	}
	due[0].TargetURL = "https://203.0.113.10/hook"
	var header string
	capture := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		header = r.Header.Get("Webhook-Signature")
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	if ok, reason := e.h.sendWebhookDelivery(context.Background(), capture, due[0]); !ok {
		t.Fatalf("send = %s", reason)
	}
	sigs := strings.Fields(header)
	if len(sigs) != 2 || !strings.HasPrefix(sigs[0], "v1,") || !strings.HasPrefix(sigs[1], "v1,") || sigs[0] == sigs[1] {
		t.Fatalf("Webhook-Signature during rotation = %q, want one per secret", header)
	}
}

// TestDeviceJoiningACustomerIsSeenByIt: a CPE that connected before any
// customer owned it (a pre-provisioned sale binds it on first contact)
// still gives its new customer a first-contact DeviceSeen.
func TestDeviceJoiningACustomerIsSeenByIt(t *testing.T) {
	e := newBindingEnv(t)
	if _, err := e.db.ExecContext(e.ctx, `UPDATE devices SET last_inform_at = now(), online_status = 'ONLINE' WHERE oui_serial = '001122-DEV-A'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(e.ctx, `UPDATE devices SET customer_id = $1 WHERE oui_serial = '001122-DEV-A'`, e.custA); err != nil {
		t.Fatal(err)
	}
	_, page := e.replay(t, bindingToken(t, e.custA, false), "?after=0")
	if len(page.Events) != 1 || page.Events[0].Type != "DeviceSeen" || !strings.Contains(string(page.Events[0].Data), `"joinedCustomer":true`) ||
		page.Events[0].OccurredAt.Location() != time.UTC {
		t.Fatalf("A's stream = %+v", page.Events)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
