package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"acs/internal/bss"
)

// TestMalformedPathIDReadsAsNotFound: a path id that is not a UUID names no
// sale, subscription or delivery, so each route answers as it does for an
// unknown id (404 ErrNotFound) and logs no error; it used to reach Postgres
// and come back as a 500 (SQLSTATE 22P02).
func TestMalformedPathIDReadsAsNotFound(t *testing.T) {
	e := newBindingEnv(t)
	e.h.webhooks = bss.NewWebhookRepository(e.db)
	var logs bytes.Buffer
	e.h.logger = slog.New(slog.NewTextHandler(&logs, nil))
	tokA := bindingToken(t, e.custA, false)
	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA, map[string]any{"account_id": "A.1", "oui_serial": "001122-DEV-A"}); rec.Code != http.StatusOK {
		t.Fatalf("mapping = %d %s", rec.Code, rec.Body)
	}
	acct := "A.1"
	sub, err := e.h.webhooks.CreateSubscription(e.ctx, &acct, "https://203.0.113.10/hook", "secret-0123456789abcdef", []string{"DeviceSeen"})
	if err != nil {
		t.Fatal(err)
	}

	const unknown = "00000000-0000-0000-0000-000000000000"
	routes := []struct {
		name, method string
		fn           http.HandlerFunc
		id, delivery string // path values; delivery only where the route has one
	}{
		{"get sale", http.MethodGet, e.h.getSale, "", ""},
		{"retry sale", http.MethodPost, e.h.retrySale, "", ""},
		{"cancel sale", http.MethodPost, e.h.cancelSale, "", ""},
		{"delete webhook", http.MethodDelete, e.h.deleteWebhookSubscription, "", ""},
		{"rotate webhook secret", http.MethodPost, e.h.rotateWebhookSecret, "", ""},
		{"list deliveries", http.MethodGet, e.h.listWebhookDeliveries, "", ""},
		{"redeliver, malformed subscription", http.MethodPost, e.h.redeliverWebhook, "", unknown},
		{"redeliver, malformed delivery", http.MethodPost, e.h.redeliverWebhook, sub.ID, "*"},
	}
	// Forms uuid.Parse or Postgres would take but the API never issues are
	// not ids either.
	malformed := []string{"not-a-uuid", "1", "urn:uuid:" + unknown, "{" + unknown + "}", strings.ReplaceAll(unknown, "-", "")}
	call := func(fn http.HandlerFunc, method, id, delivery string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", nil)
		req.SetPathValue("id", id)
		req.SetPathValue("delivery_id", delivery)
		req.Header.Set("Authorization", "Bearer "+tokA)
		rec := httptest.NewRecorder()
		fn(rec, req)
		return rec
	}
	for _, rt := range routes {
		// What the route says for a well-formed id that names nothing.
		id, delivery := rt.id, rt.delivery
		if id == "" {
			id = unknown
		}
		if delivery == "*" {
			delivery = unknown
		}
		want := call(rt.fn, rt.method, id, delivery)
		if want.Code != http.StatusNotFound || !strings.Contains(want.Body.String(), "ErrNotFound") {
			t.Fatalf("%s, unknown id = %d %s", rt.name, want.Code, want.Body)
		}
		for _, bad := range malformed {
			id, delivery := rt.id, rt.delivery
			if id == "" {
				id = bad
			}
			if delivery == "*" {
				delivery = bad
			}
			got := call(rt.fn, rt.method, id, delivery)
			if got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Errorf("%s, id %q = %d %s; want as for an unknown id: %d %s", rt.name, bad, got.Code, got.Body, want.Code, want.Body)
			}
		}
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("malformed ids were logged as errors:\n%s", logs.String())
	}
	if _, err := e.h.webhooks.SubscriptionByID(e.ctx, sub.ID); err != nil {
		t.Fatalf("subscription after the malformed calls: %v", err)
	}
}

// TestWebhookRoutesAuthorizeBeforeNotFound: like the sale routes, the
// webhook routes say nothing about a subscription id until the caller is
// authorized. Without credentials every id is 401; without the scope every
// id is 403; and another customer cannot tell a subscription that exists
// from one that does not.
func TestWebhookRoutesAuthorizeBeforeNotFound(t *testing.T) {
	e := newBindingEnv(t)
	e.h.webhooks = bss.NewWebhookRepository(e.db)
	tokA, tokB := bindingToken(t, e.custA, false), bindingToken(t, e.custB, false)
	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA, map[string]any{"account_id": "A.1", "oui_serial": "001122-DEV-A"}); rec.Code != http.StatusOK {
		t.Fatalf("mapping = %d %s", rec.Code, rec.Body)
	}
	acct := "A.1"
	sub, err := e.h.webhooks.CreateSubscription(e.ctx, &acct, "https://203.0.113.10/hook", "secret-0123456789abcdef", []string{"DeviceSeen"})
	if err != nil {
		t.Fatal(err)
	}

	const unknown = "00000000-0000-0000-0000-000000000000"
	routes := []struct {
		name, method string
		fn           http.HandlerFunc
		scope        string
	}{
		{"list deliveries", http.MethodGet, e.h.listWebhookDeliveries, bss.ScopeTMFRead},
		{"redeliver", http.MethodPost, e.h.redeliverWebhook, bss.ScopeTMFWrite},
		{"rotate webhook secret", http.MethodPost, e.h.rotateWebhookSecret, bss.ScopeTMFWrite},
		// Last: with the owner's token it would delete the subscription.
		{"delete webhook", http.MethodDelete, e.h.deleteWebhookSubscription, bss.ScopeTMFWrite},
	}
	call := func(fn http.HandlerFunc, method, id, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", nil)
		req.SetPathValue("id", id)
		req.SetPathValue("delivery_id", unknown)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		fn(rec, req)
		return rec
	}
	for _, rt := range routes {
		// A bound client holding every TMF scope but the one this route needs.
		var others []string
		for _, s := range bss.AllTMFScopes() {
			if s != rt.scope {
				others = append(others, s)
			}
		}
		unscoped := scopedToken(t, e.custA, others...)
		theirs := call(rt.fn, rt.method, sub.ID, tokB)
		if theirs.Code != http.StatusNotFound {
			t.Fatalf("%s, another customer's subscription = %d %s", rt.name, theirs.Code, theirs.Body)
		}
		for _, id := range []string{sub.ID, unknown, "not-a-uuid"} {
			if rec := call(rt.fn, rt.method, id, ""); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s, id %q without credentials = %d %s; want 401", rt.name, id, rec.Code, rec.Body)
			}
			if rec := call(rt.fn, rt.method, id, unscoped); rec.Code != http.StatusForbidden {
				t.Errorf("%s, id %q without %s = %d %s; want 403", rt.name, id, rt.scope, rec.Code, rec.Body)
			}
			if rec := call(rt.fn, rt.method, id, tokB); rec.Code != theirs.Code || rec.Body.String() != theirs.Body.String() {
				t.Errorf("%s, id %q for another customer = %d %s; want as for a subscription that is not theirs: %d %s",
					rt.name, id, rec.Code, rec.Body, theirs.Code, theirs.Body)
			}
		}
	}
	if _, err := e.h.webhooks.SubscriptionByID(e.ctx, sub.ID); err != nil {
		t.Fatalf("subscription after the refused calls: %v", err)
	}
	if rec := call(e.h.deleteWebhookSubscription, http.MethodDelete, sub.ID, tokA); rec.Code != http.StatusNoContent {
		t.Fatalf("owner deleting its subscription = %d %s", rec.Code, rec.Body)
	}
}
