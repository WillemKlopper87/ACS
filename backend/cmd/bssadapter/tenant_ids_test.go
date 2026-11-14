package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"time"

	"acs/internal/accounts"
	"acs/internal/auth"
	"acs/internal/bss"
)

// Account IDs and order IDs are unique per customer
// (docs/TENANT-SCOPED-IDS-DESIGN.md). These tests run every request
// through withAuth, as production does: that is where a bound client's
// customer is attached to the request.

func (e *bindingEnv) via(fn http.HandlerFunc) http.HandlerFunc {
	return withAuth("", bindingSecret, func(_ context.Context, _ string) bool { return false }, fn).ServeHTTP
}

// two ISPs, each with one device mapped to an account they both call "1001".
func twoISPsSameAccount(t *testing.T) (e *bindingEnv, tokA, tokB string) {
	t.Helper()
	e = newBindingEnvWithACS(t, acceptingACS())
	tokA, tokB = bindingToken(t, e.custA, false), bindingToken(t, e.custB, false)
	for tok, serial := range map[string]string{tokA: "001122-DEV-A", tokB: "001122-DEV-B"} {
		if rec := e.call(t, e.via(e.h.createMapping), http.MethodPost, "/bss/v1/mappings", tok,
			map[string]any{"account_id": "1001", "oui_serial": serial}); rec.Code != http.StatusOK {
			t.Fatalf("map %s to account 1001 = %d %s", serial, rec.Code, rec.Body)
		}
	}
	return e, tokA, tokB
}

func TestTwoISPsUseTheSameAccountAndOrderIDs(t *testing.T) {
	e, tokA, tokB := twoISPsSameAccount(t)

	// Each ISP sees its own device under "1001", and only that.
	for tok, want := range map[string]string{tokA: "001122-DEV-A", tokB: "001122-DEV-B"} {
		req := httptest.NewRequest(http.MethodGet, "/bss/v1/mappings/1001", nil)
		req.SetPathValue("account_id", "1001")
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		e.via(e.h.listMappings)(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) || strings.Count(rec.Body.String(), "001122-DEV-") != 1 {
			t.Fatalf("list 1001 = %d %s, want only %s", rec.Code, rec.Body, want)
		}
	}

	// The same order ID from both: two orders, no 409, and each ISP's
	// retry returns its own.
	order := map[string]any{"external_order_id": "ORD-1", "account_id": "1001", "action": "SUSPEND"}
	keys := map[string]string{}
	for name, tok := range map[string]string{"A": tokA, "B": tokB} {
		rec := e.call(t, e.via(e.h.createOrder), http.MethodPost, "/bss/v1/orders", tok, order)
		var resp orderResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); rec.Code != http.StatusAccepted || err != nil {
			t.Fatalf("%s orders ORD-1 = %d %s", name, rec.Code, rec.Body)
		}
		keys[name] = resp.CommandKey
	}
	var orders, devices int
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(*), count(DISTINCT device_id) FROM bss_orders WHERE external_order_id = 'ORD-1'`).Scan(&orders, &devices); err != nil || orders != 2 || devices != 2 {
		t.Fatalf("orders named ORD-1 = %d on %d devices (%v), want 2 on 2", orders, devices, err)
	}
	for name, cust := range map[string]string{"A": e.custA, "B": e.custB} {
		got, err := e.h.mappings.FindOrder(accounts.WithCustomer(e.ctx, cust), "ORD-1")
		if err != nil || got == nil || got.Scope != cust {
			t.Fatalf("%s's ORD-1 = %+v, %v", name, got, err)
		}
	}
	// Nobody who has not said which customer is handed one of the two.
	if _, err := e.h.mappings.FindOrder(e.ctx, "ORD-1"); !errors.Is(err, accounts.ErrAmbiguous) {
		t.Fatalf("unqualified lookup of a shared order ID = %v, want ErrAmbiguous", err)
	}

	// Within one ISP an order ID is still one order: reusing it for
	// another of its accounts is refused.
	if rec := e.call(t, e.via(e.h.createMapping), http.MethodPost, "/bss/v1/mappings", tokA,
		map[string]any{"account_id": "1002", "oui_serial": "001122-DEV-L"}); rec.Code != http.StatusOK {
		t.Fatalf("A maps a second account = %d %s", rec.Code, rec.Body)
	}
	rec := e.call(t, e.via(e.h.createOrder), http.MethodPost, "/bss/v1/orders", tokA,
		map[string]any{"external_order_id": "ORD-1", "account_id": "1002", "action": "SUSPEND"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ErrIdempotencyKeyReused") {
		t.Fatalf("A reuses ORD-1 on another account = %d %s, want 409", rec.Code, rec.Body)
	}
}

// One ISP using an ID first does not take it from another.
func TestAnAccountIDCannotBeSquatted(t *testing.T) {
	e := newBindingEnv(t)
	tokA, tokB := bindingToken(t, e.custA, false), bindingToken(t, e.custB, false)
	// B claims the ID A is going to use, by writing anything for it.
	if rec := e.call(t, e.via(e.h.createMapping), http.MethodPost, "/bss/v1/mappings", tokB,
		map[string]any{"account_id": "A-FUTURE-77", "oui_serial": "001122-DEV-B"}); rec.Code != http.StatusOK {
		t.Fatalf("B = %d %s", rec.Code, rec.Body)
	}
	if rec := e.call(t, e.via(e.h.createMapping), http.MethodPost, "/bss/v1/mappings", tokA,
		map[string]any{"account_id": "A-FUTURE-77", "oui_serial": "001122-DEV-A"}); rec.Code != http.StatusOK {
		t.Fatalf("A is blocked on its own account ID = %d %s", rec.Code, rec.Body)
	}
	if owner, _ := e.h.mappings.AccountOwner(accounts.WithCustomer(e.ctx, e.custA), "A-FUTURE-77"); owner != e.custA {
		t.Fatalf("A's account is owned by %q", owner)
	}
}

// A row's own ID (here a TMF638 service) does not open another customer's
// row to a caller that has an account of the same ID.
func TestARowOfAnotherCustomerIsNotFoundByItsID(t *testing.T) {
	e, tokA, tokB := twoISPsSameAccount(t)
	var serviceA string
	if err := e.db.QueryRowContext(e.ctx, `SELECT id::text FROM account_device_mappings WHERE oui_serial = '001122-DEV-A'`).Scan(&serviceA); err != nil {
		t.Fatal(err)
	}
	get := func(wrap bool, tok string) int {
		req := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceInventoryManagement/v4/service/"+serviceA, nil)
		req.SetPathValue("id", serviceA)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		if wrap {
			e.via(e.h.getTMF638Service)(rec, req)
		} else {
			e.h.getTMF638Service(rec, req)
		}
		return rec.Code
	}
	if code := get(true, tokA); code != http.StatusOK {
		t.Fatalf("A reads its service = %d", code)
	}
	if code := get(true, tokB); code != http.StatusNotFound {
		t.Fatalf("B reads A's service = %d, want 404", code)
	}
	// The account gate refuses on its own as well, without the lookup
	// having been scoped by the authentication layer.
	if code := get(false, tokB); code != http.StatusNotFound {
		t.Fatalf("B reads A's service past an unscoped lookup = %d, want 404", code)
	}
}

// A fleet-wide client has no customer of its own. It names one, or the
// ID has to be unambiguous.
func TestGlobalClientMustNameTheCustomerOfASharedID(t *testing.T) {
	e, _, _ := twoISPsSameAccount(t)
	global := bindingToken(t, "", true)
	list := func(tok, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/bss/v1/mappings/1001"+query, nil)
		req.SetPathValue("account_id", "1001")
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		e.via(e.h.listMappings)(rec, req)
		return rec
	}
	if rec := list(global, ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ErrAccountAmbiguous") {
		t.Fatalf("unqualified = %d %s, want 409 ErrAccountAmbiguous", rec.Code, rec.Body)
	}
	if rec := list(global, "?customer_id="+e.custB); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "001122-DEV-B") || strings.Contains(rec.Body.String(), "001122-DEV-A") {
		t.Fatalf("named B = %d %s", rec.Code, rec.Body)
	}
	// "none" is the accounts no customer owns: there is no such 1001.
	if rec := list(global, "?customer_id=none"); strings.Contains(rec.Body.String(), "001122-DEV-") {
		t.Fatalf("unowned 1001 lists a customer's device: %s", rec.Body)
	}

	// A client limited to a list of account IDs may not name a customer:
	// that would let it into every customer's account of a listed ID.
	listed, err := signListedToken(e.custA)
	if err != nil {
		t.Fatal(err)
	}
	if rec := list(listed, "?customer_id="+e.custB); rec.Code != http.StatusConflict {
		t.Fatalf("account-listed client naming a customer = %d %s, want 409", rec.Code, rec.Body)
	}

	// A bound client is its customer whatever the query says.
	tokA := bindingToken(t, e.custA, false)
	if rec := list(tokA, "?customer_id="+e.custB); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "001122-DEV-A") || strings.Contains(rec.Body.String(), "001122-DEV-B") {
		t.Fatalf("bound A naming B = %d %s", rec.Code, rec.Body)
	}
}

// The same event source key from two ISPs is two events.
func TestTwoISPsReportEventsWithTheSameSourceKey(t *testing.T) {
	e, tokA, tokB := twoISPsSameAccount(t)
	for name, tok := range map[string]string{"A": tokA, "B": tokB} {
		rec := e.call(t, e.via(e.h.createTMF688Event), http.MethodPost, "/tmf-api/eventManagement/v4/event", tok,
			map[string]any{"sourceKey": "ticket-42", "accountId": "1001", "eventType": "TroubleTicketOpened"})
		if rec.Code/100 != 2 {
			t.Fatalf("%s reports ticket-42 = %d %s", name, rec.Code, rec.Body)
		}
	}
	var n, customers int
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(*), count(DISTINCT customer_id) FROM tmf_events WHERE source_key = 'ticket-42'`).Scan(&n, &customers); err != nil || n != 2 || customers != 2 {
		t.Fatalf("events ticket-42 = %d for %d customers (%v), want 2 for 2", n, customers, err)
	}
}

// An operator moves an account between customers, but not onto an ID the
// customer already uses.
func TestAssigningAnAccountOntoAnExistingIDIsRefused(t *testing.T) {
	e, _, _ := twoISPsSameAccount(t)
	// Unqualified, "1001" is two accounts.
	if err := e.h.mappings.AssignAccountOwner(e.ctx, "1001", e.custA, "operator:test"); !errors.Is(err, accounts.ErrAmbiguous) {
		t.Fatalf("unqualified assign of a shared ID = %v, want ErrAmbiguous", err)
	}
	// B's 1001 cannot become A's: A has one.
	if err := e.h.mappings.AssignAccountOwner(accounts.WithCustomer(e.ctx, e.custB), "1001", e.custA, "operator:test"); !errors.Is(err, bss.ErrAccountIDTaken) {
		t.Fatalf("move onto an existing ID = %v, want ErrAccountIDTaken", err)
	}
	var owners int
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(DISTINCT customer_id) FROM bss_accounts WHERE account_id = '1001'`).Scan(&owners); err != nil || owners != 2 {
		t.Fatalf("owners of 1001 after the refused move = %d (%v)", owners, err)
	}
}

// signListedToken returns a token for a client that is not bound to a
// customer and may act on the account IDs "1001" only.
func signListedToken(_ string) (string, error) {
	now := time.Now()
	return auth.SignJWT(bindingSecret, auth.Claims{Subject: "bss-client:listed", Role: bssClientRole, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		Scopes: bss.AllTMFScopes(), AccountIDs: []string{"1001"}})
}

// acceptingACS stands in for the operator API: every job is accepted with
// a command key of its own, and every read finds an empty object.
func acceptingACS() http.Handler {
	var n atomic.Int64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"command_key": "ck-" + strconv.FormatInt(n.Add(1), 10)})
	})
}
