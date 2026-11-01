package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"acs/internal/secretstore"
)

const (
	secretTestKey  = "bss-secret-store-test-key-0123456789"
	passphrasePath = "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.KeyPassphrase"
	// canary is the value the tests look for everywhere it must not be.
	canary = "CANARY-wpa-7f3e9a1c"
)

// secretACS is an internal API stub for MODIFY_WIFI on a TR-098 device:
// it records every request body the adapter sends it.
type secretACS struct {
	mu     sync.Mutex
	bodies []string
}

func (a *secretACS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.bodies = append(a.bodies, r.Method+" "+r.URL.Path+" "+string(body))
	a.mu.Unlock()
	switch {
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/parameters"):
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"command_key": "ck-" + time.Now().Format("150405.000000000"), "status": "QUEUED"})
	case strings.HasSuffix(r.URL.Path, "/parameter-names"):
		_ = json.NewEncoder(w).Encode(map[string]any{"names": map[string]bool{passphrasePath: true}, "discovered_at": "2026-11-01T10:00:00Z"})
	default:
		_ = json.NewEncoder(w).Encode(map[string]string{"id": strings.TrimPrefix(r.URL.Path, "/api/v1/devices/"), "data_model_root": "IGD1"})
	}
}

type secretEnv struct {
	ctx  context.Context
	h    *handler
	db   *sql.DB
	acs  *secretACS
	logs *bytes.Buffer
}

func newSecretEnv(t *testing.T) *secretEnv {
	t.Helper()
	acs := &secretACS{}
	ctx, h, db := newOrderTestHandler(t, acs)
	logs := &bytes.Buffer{}
	h.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store, err := secretstore.New(db, secretTestKey)
	if err != nil {
		t.Fatal(err)
	}
	h.secrets, h.secretTTL, h.secretsBehindTLSProxy = store, defaultSecretTTL, true
	return &secretEnv{ctx: ctx, h: h, db: db, acs: acs, logs: logs}
}

func (e *secretEnv) do(t *testing.T, fn http.HandlerFunc, method, target, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, target, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if i := strings.Index(target, "/secrets/"); i >= 0 {
		req.SetPathValue("handle", target[i+len("/secrets/"):])
	}
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

func (e *secretEnv) putSecret(t *testing.T, token, value string) string {
	t.Helper()
	rec := e.do(t, e.h.createSecret, http.MethodPost, "/bss/v1/secrets", token, map[string]any{"value": value})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /bss/v1/secrets = %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("the handle response is cacheable")
	}
	var out struct {
		Handle    string    `json:"handle"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Handle == "" || out.ExpiresAt.IsZero() {
		t.Fatalf("handle response %s", rec.Body.String())
	}
	return out.Handle
}

func wifiOrder(id, account, handle string) createOrderRequest {
	return createOrderRequest{ExternalOrderID: id, AccountID: account, Action: "MODIFY_WIFI",
		Parameters: map[string]string{"wifi_password_handle": handle}}
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return p.Code
}

// TestSecretCanaryNeverStored is the I4 canary: a passphrase sent as a
// handle reaches no table, log line or internal API request in clear,
// and opens once, for the order's device only.
func TestSecretCanaryNeverStored(t *testing.T) {
	e := newSecretEnv(t)
	const account, device = "acct-canary", "11111111-1111-1111-1111-111111111111"
	seedOrderDevice(t, e.ctx, e.db, account, device)

	handle := e.putSecret(t, "", canary)
	rec := e.do(t, e.h.preflightAction, http.MethodPost, "/bss/v1/actions/preflight", "",
		actionPreflightRequest{AccountID: account, Action: "MODIFY_WIFI", Parameters: map[string]string{"wifi_password_handle": handle}})
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight = %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(t, e.h.createOrder, http.MethodPost, "/bss/v1/orders", "", wifiOrder("ord-canary", account, handle))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("order = %d %s", rec.Code, rec.Body.String())
	}

	// The internal API got the handle, not the value.
	sentHandle := false
	for _, b := range e.acs.bodies {
		if strings.Contains(b, canary) {
			t.Fatalf("the value reached the internal API: %s", b)
		}
		if strings.HasPrefix(b, "PUT ") && strings.Contains(b, `"secret_handle":"`+handle+`"`) {
			sentHandle = true
		}
	}
	if !sentHandle {
		t.Fatalf("no parameter write named the handle: %v", e.acs.bodies)
	}
	if strings.Contains(e.logs.String(), canary) {
		t.Fatal("the value was logged")
	}
	assertNotInDatabase(t, e.ctx, e.db, canary)

	// A second order cannot use the claimed handle.
	rec = e.do(t, e.h.createOrder, http.MethodPost, "/bss/v1/orders", "", wifiOrder("ord-canary-2", account, handle))
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "ErrSecretHandleInvalid" {
		t.Fatalf("reused handle = %d %s", rec.Code, rec.Body.String())
	}
	// The gateway opens it once, for the order's device.
	if _, err := e.h.secrets.Open(e.ctx, handle, "22222222-2222-2222-2222-222222222222"); err == nil {
		t.Fatal("the handle opened for another device")
	}
	if v, err := e.h.secrets.Open(e.ctx, handle, device); err != nil || v != canary {
		t.Fatalf("Open = %q, %v", v, err)
	}
	if _, err := e.h.secrets.Open(e.ctx, handle, device); err == nil {
		t.Fatal("the handle opened twice")
	}
}

// assertNotInDatabase scans every column of every table as text.
func assertNotInDatabase(t *testing.T, ctx context.Context, db *sql.DB, needle string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	if len(tables) < 50 {
		t.Fatalf("only %d tables scanned", len(tables))
	}
	for _, table := range tables {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM "`+table+`" t WHERE t::text LIKE '%' || $1 || '%'`, needle).Scan(&n); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		if n > 0 {
			t.Fatalf("the value is stored in clear in %s", table)
		}
	}
}

func TestSecretOwnership(t *testing.T) {
	e := newSecretEnv(t)
	e.h.oauthSigningSecret = bindingSecret
	var custA, custB string
	for _, c := range []*string{&custA, &custB} {
		if err := e.db.QueryRowContext(e.ctx, `INSERT INTO customers (id, name) VALUES (gen_random_uuid(), 'isp') RETURNING id`).Scan(c); err != nil {
			t.Fatal(err)
		}
	}
	tokA, tokB := bindingToken(t, custA, false), bindingToken(t, custB, false)
	const deviceB = "33333333-3333-3333-3333-333333333333"
	seedOrderDevice(t, e.ctx, e.db, "acct-b", deviceB)
	if _, err := e.db.ExecContext(e.ctx, `INSERT INTO bss_accounts (account_id, customer_id, claimed_by) VALUES ('acct-b', $1, 'test')`, custB); err != nil {
		t.Fatal(err)
	}

	handleA := e.putSecret(t, tokA, "value-of-a")
	rec := e.do(t, e.h.createOrder, http.MethodPost, "/bss/v1/orders", tokB, wifiOrder("ord-b", "acct-b", handleA))
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "ErrSecretHandleInvalid" {
		t.Fatalf("customer B used A's handle: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, e.h.deleteSecret, http.MethodDelete, "/bss/v1/secrets/"+handleA, tokB, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("B deleted A's handle: %d", rec.Code)
	}
	handleB := e.putSecret(t, tokB, "value-of-b")
	if rec := e.do(t, e.h.createOrder, http.MethodPost, "/bss/v1/orders", tokB, wifiOrder("ord-b2", "acct-b", handleB)); rec.Code != http.StatusAccepted {
		t.Fatalf("B's own handle = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, e.h.deleteSecret, http.MethodDelete, "/bss/v1/secrets/"+handleA, tokA, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("A's delete = %d", rec.Code)
	}
	if rec := e.do(t, e.h.deleteSecret, http.MethodDelete, "/bss/v1/secrets/"+handleA, tokA, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d", rec.Code)
	}
	// An account-listed client (no customer, not global) gets no handles.
	listed := bindingToken(t, "", false)
	if rec := e.do(t, e.h.createSecret, http.MethodPost, "/bss/v1/secrets", listed, map[string]any{"value": "x"}); rec.Code != http.StatusForbidden {
		t.Fatalf("account-listed client = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSecretRequestRules(t *testing.T) {
	e := newSecretEnv(t)

	e.h.secretsBehindTLSProxy = false
	rec := e.do(t, e.h.createSecret, http.MethodPost, "/bss/v1/secrets", "", map[string]any{"value": "x"})
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != "ErrTLSRequired" {
		t.Fatalf("plaintext = %d %s", rec.Code, rec.Body.String())
	}
	raw, _ := json.Marshal(map[string]any{"value": "x"})
	req := httptest.NewRequest(http.MethodPost, "/bss/v1/secrets", bytes.NewReader(raw))
	req.TLS = &tls.ConnectionState{}
	tlsRec := httptest.NewRecorder()
	e.h.createSecret(tlsRec, req)
	if tlsRec.Code != http.StatusCreated {
		t.Fatalf("over TLS = %d %s", tlsRec.Code, tlsRec.Body.String())
	}
	e.h.secretsBehindTLSProxy = true

	for name, body := range map[string]map[string]any{
		"empty":     {"value": ""},
		"too long":  {"value": strings.Repeat("x", 257)},
		"short ttl": {"value": "x", "ttl_seconds": 30},
		"long ttl":  {"value": "x", "ttl_seconds": 3600},
	} {
		if rec := e.do(t, e.h.createSecret, http.MethodPost, "/bss/v1/secrets", "", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, rec.Code)
		}
	}
	if rec := e.do(t, e.h.createSecret, http.MethodPost, "/bss/v1/secrets", "", map[string]any{"value": "x", "ttl_seconds": 120}); rec.Code != http.StatusCreated {
		t.Errorf("ttl 120 = %d %s", rec.Code, rec.Body.String())
	}

	keyless, _ := secretstore.New(e.db, "")
	e.h.secrets = keyless
	if rec := e.do(t, e.h.createSecret, http.MethodPost, "/bss/v1/secrets", "", map[string]any{"value": "x"}); rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != "ErrSecretStoreDisabled" {
		t.Fatalf("keyless = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSecretHandleInOrders(t *testing.T) {
	e := newSecretEnv(t)
	const account, device = "acct-rules", "44444444-4444-4444-4444-444444444444"
	seedOrderDevice(t, e.ctx, e.db, account, device)

	both := createOrderRequest{ExternalOrderID: "ord-both", AccountID: account, Action: "MODIFY_WIFI",
		Parameters: map[string]string{"wifi_password": "plain-value", "wifi_password_handle": "sh_x"}}
	if rec := e.do(t, e.h.createOrder, http.MethodPost, "/bss/v1/orders", "", both); rec.Code != http.StatusBadRequest {
		t.Fatalf("value and handle = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, e.h.createOrder, http.MethodPost, "/bss/v1/orders", "", wifiOrder("ord-unknown", account, "sh_unknown")); problemCode(t, rec) != "ErrSecretHandleInvalid" {
		t.Fatalf("unknown handle = %d %s", rec.Code, rec.Body.String())
	}

	// Handles required: a plain passphrase is refused with the code an
	// existing call already had, and a handle still works.
	e.h.requireSecretHandles = true
	plain := createOrderRequest{ExternalOrderID: "ord-plain", AccountID: account, Action: "MODIFY_WIFI",
		Parameters: map[string]string{"wifi_password": "plain-value"}}
	rec := e.do(t, e.h.createOrder, http.MethodPost, "/bss/v1/orders", "", plain)
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "ErrInvalidRequest" || !strings.Contains(rec.Body.String(), "wifi_password_handle") {
		t.Fatalf("plain passphrase with handles required = %d %s", rec.Code, rec.Body.String())
	}
	handle := e.putSecret(t, "", "sealed-value")
	if rec := e.do(t, e.h.createOrder, http.MethodPost, "/bss/v1/orders", "", wifiOrder("ord-sealed", account, handle)); rec.Code != http.StatusAccepted {
		t.Fatalf("handle with handles required = %d %s", rec.Code, rec.Body.String())
	}
	order, err := e.h.mappings.FindOrder(e.ctx, "ord-sealed")
	if err != nil || order == nil {
		t.Fatal(err)
	}
	for _, p := range order.Parameters {
		if p.Name == passphrasePath && (p.Value != "" || p.SecretHandle != handle) {
			t.Fatalf("stored write %+v, want the handle and no value", p)
		}
	}
}

func TestTMF640PatchSecretHandle(t *testing.T) {
	action, params, err := interpretTMF640Patch([]byte(`{"serviceCharacteristic":[{"name":"WiFiPasswordHandle","value":"sh_abc"}]}`))
	if err != nil || action != "MODIFY_WIFI" || params["wifi_password_handle"] != "sh_abc" || params["wifi_password"] != "" {
		t.Fatalf("interpret = %q %v %v", action, params, err)
	}
}
