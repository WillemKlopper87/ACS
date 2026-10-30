package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/bss"
	"acs/internal/onboarding"
)

var bindingSecret = []byte("binding-test-signing-secret-0123456789")

func bindingToken(t *testing.T, customerID string, global bool) string {
	t.Helper()
	now := time.Now()
	tok, err := auth.SignJWT(bindingSecret, auth.Claims{Subject: "bss-client:test", Role: bssClientRole, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		Scopes: bss.AllTMFScopes(), GlobalAccess: global, CustomerID: customerID})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type bindingEnv struct {
	ctx          context.Context
	h            *handler
	db           *sql.DB
	custA, custB string
}

func newBindingEnv(t *testing.T) *bindingEnv {
	t.Helper()
	ctx, h, db := newOrderTestHandler(t, acsRouterStub(t, nil, nil))
	h.oauthSigningSecret = bindingSecret
	h.onboarding = onboarding.NewRepository(db)
	e := &bindingEnv{ctx: ctx, h: h, db: db}
	for _, c := range []*string{&e.custA, &e.custB} {
		if err := db.QueryRowContext(ctx, `INSERT INTO customers (id, name) VALUES (gen_random_uuid(), 'isp') RETURNING id`).Scan(c); err != nil {
			t.Fatal(err)
		}
	}
	for _, serial := range []string{"DEV-A", "DEV-B", "DEV-L"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES (gen_random_uuid(), $1)`, "001122-"+serial); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *bindingEnv) call(t *testing.T, fn http.HandlerFunc, method, target, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

func (e *bindingEnv) deviceCustomer(t *testing.T, serial string) string {
	t.Helper()
	var c sql.NullString
	if err := e.db.QueryRowContext(e.ctx, `SELECT customer_id::text FROM devices WHERE oui_serial = $1`, "001122-"+serial).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c.String
}

// TestCustomerBoundClientsAreIsolated is I1's core promise: an ISP's client
// works only on its own accounts and devices, and sees another ISP's as
// not found.
func TestCustomerBoundClientsAreIsolated(t *testing.T) {
	e := newBindingEnv(t)
	tokA, tokB := bindingToken(t, e.custA, false), bindingToken(t, e.custB, false)

	// A maps an unassigned device: A owns the account and the device.
	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA,
		map[string]any{"account_id": "A.1", "oui_serial": "001122-DEV-A"}); rec.Code != http.StatusOK {
		t.Fatalf("A maps its device = %d %s", rec.Code, rec.Body)
	}
	if owner, _ := e.h.mappings.AccountOwner(e.ctx, "A.1"); owner != e.custA {
		t.Fatalf("owner of A.1 = %q, want A", owner)
	}
	if got := e.deviceCustomer(t, "DEV-A"); got != e.custA {
		t.Fatalf("device customer = %q, want A", got)
	}

	// B can neither read nor write A's account, and gets 404, not 403.
	if rec := e.call(t, e.h.listTMF641Orders, http.MethodGet, "/tmf-api/serviceOrdering/v4/serviceOrder?accountId=A.1", tokB, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("B reads A.1 = %d", rec.Code)
	}
	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokB,
		map[string]any{"account_id": "A.1", "oui_serial": "001122-DEV-B", "role": "extender"}); rec.Code != http.StatusNotFound {
		t.Fatalf("B writes A.1 = %d %s", rec.Code, rec.Body)
	}
	// Nor map A's device into its own account: the device is not found.
	rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokB, map[string]any{"account_id": "B.1", "oui_serial": "001122-DEV-A"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "ErrDeviceNotMapped") {
		t.Fatalf("B maps A's device = %d %s", rec.Code, rec.Body)
	}
	if got := e.deviceCustomer(t, "DEV-A"); got != e.custA {
		t.Fatalf("device moved to %q", got)
	}
	// A failed mapping leaves an unassigned device unassigned.
	if _, err := e.db.ExecContext(e.ctx, `INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, role, status)
		SELECT gen_random_uuid(), 'B.2', id, oui_serial, 'gateway', 'ACTIVE' FROM devices WHERE oui_serial = '001122-DEV-L'`); err != nil {
		t.Fatal(err)
	}
	if err := e.h.mappings.AssignAccountOwner(e.ctx, "B.2", e.custB, "test"); err != nil {
		t.Fatal(err)
	}
	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokB,
		map[string]any{"account_id": "B.2", "oui_serial": "001122-DEV-B"}); rec.Code != http.StatusConflict {
		t.Fatalf("role already taken = %d %s", rec.Code, rec.Body)
	}
	if got := e.deviceCustomer(t, "DEV-B"); got != "" {
		t.Fatalf("a refused mapping assigned the device to %q", got)
	}
}

// TestLegacyAccountsNeedAnOperator: an account used before binding (by a
// global client) is never claimed by a bound client; an operator assigns it.
func TestLegacyAccountsNeedAnOperator(t *testing.T) {
	e := newBindingEnv(t)
	tokA, global := bindingToken(t, e.custA, false), bindingToken(t, "", true)
	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", global,
		map[string]any{"account_id": "LEGACY-1", "oui_serial": "001122-DEV-L"}); rec.Code != http.StatusOK {
		t.Fatalf("global client maps = %d %s", rec.Code, rec.Body)
	}
	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA,
		map[string]any{"account_id": "LEGACY-1", "oui_serial": "001122-DEV-A", "role": "extender"}); rec.Code != http.StatusNotFound {
		t.Fatalf("bound client claims a legacy account = %d %s", rec.Code, rec.Body)
	}
	if err := e.h.mappings.AssignAccountOwner(e.ctx, "LEGACY-1", e.custA, "operator:test"); err != nil {
		t.Fatal(err)
	}
	if rec := e.call(t, e.h.listTMF641Orders, http.MethodGet, "/tmf-api/serviceOrdering/v4/serviceOrder?accountId=LEGACY-1", tokA, nil); rec.Code != http.StatusOK {
		t.Fatalf("after operator assignment = %d %s", rec.Code, rec.Body)
	}
	// Global clients keep working on everything.
	if rec := e.call(t, e.h.listTMF641Orders, http.MethodGet, "/tmf-api/serviceOrdering/v4/serviceOrder?accountId=LEGACY-1", global, nil); rec.Code != http.StatusOK {
		t.Fatalf("global client = %d", rec.Code)
	}
}

// TestPreProvisionSellsForTheAccountOwner: a bound client's sale belongs
// to its customer, only its own or shared plans apply, and a named
// customer must be its own.
func TestPreProvisionSellsForTheAccountOwner(t *testing.T) {
	e := newBindingEnv(t)
	tokA := bindingToken(t, e.custA, false)
	if _, err := e.h.onboarding.CreatePlan(e.ctx, onboarding.ServicePlan{CustomerID: &e.custB, Code: "HOME", Name: "B home"}); err != nil {
		t.Fatal(err)
	}
	rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA,
		map[string]any{"account_id": "A.9", "oui_serial": "001122-NEW1", "service_plan": "HOME", "pre_provision": true})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("pre-provision = %d %s", rec.Code, rec.Body)
	}
	o, err := e.h.onboarding.OpenBySerial(e.ctx, "NEW1")
	if err != nil || o.CustomerID == nil || *o.CustomerID != e.custA || o.ServicePlanID != nil {
		t.Fatalf("sale = %+v, %v; want customer A and not B's plan", o, err)
	}
	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA,
		map[string]any{"account_id": "A.10", "oui_serial": "001122-NEW2", "pre_provision": true, "customer_id": e.custB}); rec.Code != http.StatusConflict {
		t.Fatalf("pre-provision naming another customer = %d %s", rec.Code, rec.Body)
	}
}

// TestRequireCustomerBoundClients: with the production guard on, only a
// customer-bound OAuth token gets in.
func TestRequireCustomerBoundClients(t *testing.T) {
	e := newBindingEnv(t)
	e.h.requireCustomerBound = true
	e.h.token = "legacy-shared-token-0123456789"
	list := "/tmf-api/serviceOrdering/v4/serviceOrder?accountId=A.1"
	if rec := e.call(t, e.h.listTMF641Orders, http.MethodGet, list, bindingToken(t, "", true), nil); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "ErrClientNotCustomerBound") {
		t.Fatalf("global client = %d %s", rec.Code, rec.Body)
	}
	if rec := e.call(t, e.h.listTMF641Orders, http.MethodGet, list, e.h.token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("shared token = %d", rec.Code)
	}
	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", e.h.token, map[string]any{"account_id": "A.1", "oui_serial": "001122-DEV-A"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("shared token on /bss/v1 = %d", rec.Code)
	}
	tokA := bindingToken(t, e.custA, false)
	if rec := e.call(t, e.h.createMapping, http.MethodPost, "/bss/v1/mappings", tokA, map[string]any{"account_id": "A.1", "oui_serial": "001122-DEV-A"}); rec.Code != http.StatusOK {
		t.Fatalf("bound client = %d %s", rec.Code, rec.Body)
	}
	if e.h.authorizeBSSFleet(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/bss/v1/webhooks", nil)) {
		t.Fatal("fleet-wide operations allowed under the guard")
	}
}

// TestTokenCarriesTheCustomer: a bound client's token says which customer
// it is bound to, in the JWT and in the token response.
func TestTokenCarriesTheCustomer(t *testing.T) {
	e := newBindingEnv(t)
	e.h.oauthClients = bss.NewOAuthRepository(e.db)
	client, secret, err := e.h.oauthClients.CreateClientWithPolicy(e.ctx, "isp-a", bss.OAuthPolicy{Scopes: []string{bss.ScopeTMFRead}, CustomerID: &e.custA})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {client.ClientID}, "client_secret": {secret}}
	req := httptest.NewRequest(http.MethodPost, "/bss/v1/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.h.issueOAuthToken(rec, req)
	var resp tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.CustomerID != e.custA {
		t.Fatalf("token response = %s", rec.Body)
	}
	claims, err := auth.VerifyJWT(bindingSecret, resp.AccessToken)
	if err != nil || claims.CustomerID != e.custA || claims.GlobalAccess {
		t.Fatalf("claims = %+v, %v", claims, err)
	}
	if _, _, err := e.h.oauthClients.CreateClientWithPolicy(e.ctx, "bad", bss.OAuthPolicy{Scopes: []string{bss.ScopeTMFRead}, CustomerID: &e.custA, GlobalAccess: true}); err == nil {
		t.Fatal("a client both global and customer-bound was accepted")
	}
}
