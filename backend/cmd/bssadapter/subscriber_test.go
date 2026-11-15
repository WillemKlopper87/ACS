package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"acs/internal/subscribers"
)

// TestBSSSetsSubscriberProfile: the BSS names the subscriber and sets VIP
// on a mapping or on its own; its name replaces an operator's; another
// customer's client cannot touch the account.
func TestBSSSetsSubscriberProfile(t *testing.T) {
	e := newBindingEnv(t)
	e.h.subscribers = subscribers.NewRepository(e.db)
	tokA, tokB := bindingToken(t, e.custA, false), bindingToken(t, e.custB, false)

	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA,
		map[string]any{"account_id": "A.1", "oui_serial": "001122-DEV-A", "subscriber_name": "Thandi Nkosi", "vip": true}); rec.Code != http.StatusOK {
		t.Fatalf("mapping with subscriber = %d %s", rec.Code, rec.Body)
	}
	p, err := e.h.subscribers.Get(e.ctx, "A.1")
	if err != nil || p == nil || p.DisplayName == nil || *p.DisplayName != "Thandi Nkosi" || p.NameSource != subscribers.SourceBSS || !p.VIP {
		t.Fatalf("profile after mapping = %+v %v", p, err)
	}

	put := func(tok, account string, body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/bss/v1/accounts/"+account+"/subscriber", bytes.NewReader(b))
		req.SetPathValue("account_id", account)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		e.h.putAccountSubscriber(rec, req)
		return rec
	}
	if rec := put(tokA, "A.1", map[string]any{"vip": false}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"vip":false`) ||
		!strings.Contains(rec.Body.String(), `"subscriber_name":"Thandi Nkosi"`) {
		t.Fatalf("clear vip = %d %s", rec.Code, rec.Body)
	}
	// "A.1" from B names B's own account of that ID: B's write lands
	// there and A's subscriber is unchanged.
	if rec := put(tokB, "A.1", map[string]any{"subscriber_name": "Someone"}); rec.Code != http.StatusOK {
		t.Fatalf("B names a subscriber on its own A.1 = %d %s", rec.Code, rec.Body)
	}
	if rec := put(tokA, "A.1", map[string]any{"vip": false}); !strings.Contains(rec.Body.String(), `"subscriber_name":"Thandi Nkosi"`) {
		t.Fatalf("A's subscriber changed by B's write: %s", rec.Body)
	}
	if rec := put(tokA, "A.1", map[string]any{"nickname": "x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", rec.Code)
	}
	if rec := put(tokA, "A.1", map[string]any{"subscriber_name": strings.Repeat("x", 121)}); rec.Code != http.StatusBadRequest {
		t.Fatalf("long name = %d", rec.Code)
	}

	// An operator's name is replaced by the BSS's.
	op := "Typed By Operator"
	if _, err := e.h.subscribers.Set(e.ctx, "A.2", subscribers.SourceOperator, "op", &op, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.h.mappings.AssignAccountOwner(e.ctx, "A.2", e.custA, "test"); err != nil {
		t.Fatal(err)
	}
	if rec := put(tokA, "A.2", map[string]any{"subscriber_name": "Sipho Dlamini"}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Sipho Dlamini") {
		t.Fatalf("BSS replaces operator name = %d %s", rec.Code, rec.Body)
	}
}
