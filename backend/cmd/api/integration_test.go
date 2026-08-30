package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/bss"
	"acs/internal/cliaccess"
	"acs/internal/credentials"
	"acs/internal/dashboard"
	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/firmware"
	"acs/internal/jobs"
	"acs/internal/mailer"
	"acs/internal/observability"
	"acs/internal/operators"
	"acs/internal/parameters"
	"acs/internal/policy"
	"acs/internal/rollout"
	"acs/internal/scheduler"
	"acs/internal/store"
	"acs/internal/templates"
	"acs/internal/tenancy"
	"acs/internal/transfer"
	"acs/internal/uploads"
	"acs/internal/vpn"

	"database/sql"

	"golang.org/x/crypto/bcrypt"
)

// Integration tests against a real Postgres (audit P2.1): the actual
// router, middleware, repositories, and migrations, driven over HTTP.
// They run only when ACS_TEST_POSTGRES_DSN is set (CI's migrations job
// does; locally, point it at a throwaway database — the schema is
// dropped and recreated). Everything else in this package stays a fast
// unit test.

var testJWTSecret = []byte("integration-test-jwt-secret-0123456789abcdef")

const testServiceToken = "integration-test-service-token-0123456789abcdef"

type testEnv struct {
	t      *testing.T
	ctx    context.Context
	db     *sql.DB
	h      *handler
	srv    *httptest.Server
	tokens map[string]string // username -> session JWT
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fwRoot, upRoot := t.TempDir(), t.TempDir()
	fwFS, _ := firmware.NewStorage(fwRoot)
	upFS, _ := uploads.NewStorage(upRoot)
	credRepo, _ := credentials.NewRepository(db, "integration-test-encryption-key")
	cliRepo, _ := cliaccess.NewRepository(db, "integration-test-encryption-key")
	_, overlay, _ := net.ParseCIDR("10.99.0.0/16")
	vpnRepo, _ := vpn.NewRepository(db, "integration-test-encryption-key", overlay)
	metrics := observability.NewMetrics("api-test")

	h := &handler{
		logger:          logger,
		devices:         devices.NewRepository(db),
		jobs:            jobs.NewRepository(db),
		params:          parameters.NewRepository(db),
		vendors:         adapters.NewRegistry(),
		auditor:         observability.NewAuditor(db),
		firmware:        firmware.NewRepository(db),
		firmwareFS:      fwFS,
		firmwareBase:    "http://acs.test",
		operators:       operators.NewRepository(db),
		jwtSecret:       testJWTSecret,
		transferKey:     transfer.DeriveKey(testJWTSecret),
		uploadMaxBytes:  1 << 20,
		metrics:         metrics,
		groups:          devices.NewGroupRepository(db),
		credentials:     credRepo,
		schedules:       scheduler.NewRepository(db),
		rollouts:        rollout.NewRepository(db),
		policies:        policy.NewRepository(db),
		uploads:         uploads.NewRepository(db),
		uploadsFS:       upFS,
		uploadsBase:     "http://acs.test",
		templates:       templates.NewRepository(db),
		cli:             cliRepo,
		permissions:     operators.NewPermissionRepository(db),
		mailer:          mailer.New(mailer.Config{}, logger),
		frontendBaseURL: "http://console.test",
		tenancy:         tenancy.NewRepository(db),
		dashboards:      dashboard.NewRepository(db),
		bssMappings:     bss.NewRepository(db),
		bssWebhooks:     bss.NewWebhookRepository(db),
		bssOAuthClients: bss.NewOAuthRepository(db),
		bssHTTPClient:   &http.Client{Timeout: time.Second},
		vpnPeers:        vpnRepo,
	}
	mux := h.registerRoutes(metrics, db)
	srv := httptest.NewServer(withJWTAuth(testJWTSecret, testServiceToken, withBodyLimit(mux)))
	t.Cleanup(srv.Close)

	env := &testEnv{t: t, ctx: ctx, db: db, h: h, srv: srv, tokens: map[string]string{}}
	return env
}

func (e *testEnv) operator(username, role string, scopes ...tenancy.Scope) {
	e.t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw-"+username), bcrypt.MinCost)
	op, err := e.h.operators.Create(e.ctx, username, "", string(hash), role)
	if err != nil {
		e.t.Fatalf("create operator %s: %v", username, err)
	}
	if len(scopes) > 0 {
		if err := e.h.tenancy.SetOperatorScopes(e.ctx, op.ID, scopes); err != nil {
			e.t.Fatalf("scope %s: %v", username, err)
		}
	}
	now := time.Now().UTC()
	tok, _ := auth.SignJWT(testJWTSecret, auth.Claims{Subject: username, Role: role, IssuedAt: now, ExpiresAt: now.Add(time.Hour)})
	e.tokens[username] = tok
}

func (e *testEnv) customer(name string) string {
	e.t.Helper()
	c, err := e.h.tenancy.CreateCustomer(e.ctx, name, nil)
	if err != nil {
		e.t.Fatalf("create customer: %v", err)
	}
	return c.ID
}

func (e *testEnv) device(serial string, customerID *string) string {
	e.t.Helper()
	d, err := e.h.devices.PreRegister(e.ctx, "ABCDEF-"+serial, "TestVendor", "ABCDEF", "CPE", serial, customerID, nil)
	if err != nil {
		e.t.Fatalf("pre-register %s: %v", serial, err)
	}
	return d.ID
}

type resp struct {
	code int
	body string
}

func (e *testEnv) call(as, method, path string, body any) resp {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	if as != "" {
		if tok, ok := e.tokens[as]; ok {
			req.Header.Set("Authorization", "Bearer "+tok)
		} else {
			req.Header.Set("Authorization", "Bearer "+as) // raw token (service token, ticket)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, string(b)}
}

func (e *testEnv) grant(role string, perms ...string) {
	for _, p := range perms {
		if err := e.h.permissions.Set(e.ctx, role, p, true); err != nil {
			e.t.Fatalf("grant %s to %s: %v", p, role, err)
		}
	}
}

// TestIntegration_TenantIsolation is the two-tenant negative suite the
// audit asked for (P0.2 acceptance gate): a scoped operator must not
// read, act on, list, or aggregate anything outside their customer set,
// including unassigned devices; a superadmin sees everything; and every
// denial is a 404 indistinguishable from a nonexistent ID.
func TestIntegration_TenantIsolation(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("Customer A"), e.customer("Customer B")
	devA, devB, devU := e.device("A001", &custA), e.device("B001", &custB), e.device("U001", nil)
	e.operator("root", operators.RoleSuperAdmin)
	e.operator("alice", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.operator("bob", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custB})
	e.grant(operators.RoleNOC, operators.PermDevicesWrite, operators.PermBulkActions, operators.PermUploadRequest, operators.PermConnectionReq)

	t.Run("single device read", func(t *testing.T) {
		cases := []struct {
			as, dev string
			want    int
		}{
			{"alice", devA, 200}, {"alice", devB, 404}, {"alice", devU, 404},
			{"bob", devB, 200}, {"bob", devA, 404},
			{"root", devA, 200}, {"root", devB, 200}, {"root", devU, 200},
		}
		for _, c := range cases {
			if got := e.call(c.as, "GET", "/api/v1/devices/"+c.dev, nil); got.code != c.want {
				t.Errorf("%s GET device %s → %d, want %d (%s)", c.as, c.dev, got.code, c.want, strings.TrimSpace(got.body))
			}
		}
	})

	t.Run("device list, summary, matching ids are scoped", func(t *testing.T) {
		r := e.call("alice", "GET", "/api/v1/devices", nil)
		if r.code != 200 || !strings.Contains(r.body, devA) || strings.Contains(r.body, devB) || strings.Contains(r.body, devU) {
			t.Errorf("alice's device list leaks: %d %s", r.code, r.body)
		}
		r = e.call("alice", "GET", "/api/v1/devices/ids", nil)
		if r.code != 200 || !strings.Contains(r.body, devA) || strings.Contains(r.body, devB) {
			t.Errorf("alice's matching ids leak: %d %s", r.code, r.body)
		}
		r = e.call("alice", "GET", "/api/v1/devices/summary", nil)
		if r.code != 200 || !strings.Contains(r.body, `"Count":1`) {
			t.Errorf("alice's summary should count exactly her one device: %d %s", r.code, r.body)
		}
		r = e.call("root", "GET", "/api/v1/devices", nil)
		if !strings.Contains(r.body, `"total":3`) {
			t.Errorf("root should see all 3 devices: %s", r.body)
		}
	})

	t.Run("mutations on a foreign device are 404", func(t *testing.T) {
		params := map[string]any{"parameters": []map[string]string{{"name": "Device.X", "value": "1", "type": "xsd:string"}}}
		if r := e.call("alice", "PUT", "/api/v1/devices/"+devB+"/parameters", params); r.code != 404 {
			t.Errorf("alice PUT parameters on bob's device → %d, want 404", r.code)
		}
		if r := e.call("alice", "PUT", "/api/v1/devices/"+devA+"/parameters", params); r.code != 202 {
			t.Errorf("alice PUT parameters on own device → %d, want 202 (%s)", r.code, r.body)
		}
		if r := e.call("alice", "POST", "/api/v1/devices/"+devU+"/connection-request", nil); r.code != 404 {
			t.Errorf("alice connection-request on unassigned device → %d, want 404", r.code)
		}
	})

	t.Run("bulk action skips foreign devices per-device", func(t *testing.T) {
		r := e.call("alice", "POST", "/api/v1/devices/bulk-actions", map[string]any{
			"action": "CONNECTION_REQUEST", "device_ids": []string{devA, devB, devU},
		})
		if r.code != 202 {
			t.Fatalf("bulk → %d %s", r.code, r.body)
		}
		var out struct {
			Results []struct {
				DeviceID string `json:"device_id"`
				Error    string `json:"error"`
			} `json:"results"`
		}
		_ = json.Unmarshal([]byte(r.body), &out)
		for _, res := range out.Results {
			foreign := res.DeviceID != devA
			if foreign && res.Error != "not found" {
				t.Errorf("foreign device %s in bulk action: error=%q, want \"not found\"", res.DeviceID, res.Error)
			}
			if !foreign && res.Error != "" {
				t.Errorf("own device %s in bulk action failed: %q", res.DeviceID, res.Error)
			}
		}
	})

	t.Run("jobs are scoped by device", func(t *testing.T) {
		jobB, err := e.h.jobs.Create(e.ctx, devB, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: []string{"Device."}}, "bob")
		if err != nil {
			t.Fatal(err)
		}
		if r := e.call("alice", "GET", "/api/v1/jobs/"+jobB.CommandKey, nil); r.code != 404 {
			t.Errorf("alice GET bob's job → %d, want 404", r.code)
		}
		if r := e.call("bob", "GET", "/api/v1/jobs/"+jobB.CommandKey, nil); r.code != 200 {
			t.Errorf("bob GET own job → %d, want 200", r.code)
		}
		if r := e.call("alice", "GET", "/api/v1/jobs", nil); strings.Contains(r.body, jobB.CommandKey) {
			t.Errorf("alice's fleet job list leaks bob's job: %s", r.body)
		}
		if r := e.call("root", "GET", "/api/v1/jobs", nil); !strings.Contains(r.body, jobB.CommandKey) {
			t.Errorf("root's job list should include bob's job")
		}
	})

	t.Run("dashboard aggregates are scoped", func(t *testing.T) {
		r := e.call("alice", "GET", "/api/v1/dashboard", nil)
		if r.code != 200 || !strings.Contains(r.body, `"scoped":true`) {
			t.Fatalf("dashboard → %d %s", r.code, r.body)
		}
		var out struct {
			ByStatus map[string]int `json:"devices_by_status"`
		}
		_ = json.Unmarshal([]byte(r.body), &out)
		total := 0
		for _, n := range out.ByStatus {
			total += n
		}
		if total != 1 {
			t.Errorf("alice's dashboard counts %d devices, want 1", total)
		}
	})
}

// TestIntegration_TransferTokens exercises the public upload receipt
// end to end (P0.3): the slot URL's token is required, purpose-bound,
// single-use, and size-capped.
func TestIntegration_TransferTokens(t *testing.T) {
	e := newTestEnv(t)
	custA := e.customer("Customer A")
	devA := e.device("A001", &custA)
	e.operator("alice", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.grant(operators.RoleNOC, operators.PermUploadRequest)

	r := e.call("alice", "POST", "/api/v1/devices/"+devA+"/uploads", map[string]string{"file_type": "1 Vendor Configuration File"})
	if r.code != 202 {
		t.Fatalf("create upload → %d %s", r.code, r.body)
	}
	var created struct {
		UploadID string `json:"upload_id"`
	}
	_ = json.Unmarshal([]byte(r.body), &created)
	slot, err := e.h.uploads.ByID(e.ctx, created.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := e.h.jobs.List(e.ctx, devA, nil, false)
	var payload jobs.UploadPayload
	_ = json.Unmarshal(job[0].Payload, &payload)
	if !strings.Contains(payload.URL, "?token=") {
		t.Fatalf("upload URL handed to the CPE carries no token: %s", payload.URL)
	}
	token := payload.URL[strings.Index(payload.URL, "?token=")+len("?token="):]
	put := func(tok string, body []byte) int {
		url := e.srv.URL + "/api/v1/uploads/" + slot.ID + "/receive"
		if tok != "" {
			url += "?token=" + tok
		}
		req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if got := put("", []byte("cfg")); got != 403 {
		t.Errorf("PUT without token → %d, want 403", got)
	}
	if got := put(transfer.Sign(e.h.transferKey, "firmware", slot.ID, time.Now().Add(time.Hour)), []byte("cfg")); got != 403 {
		t.Errorf("PUT with a firmware-purpose token → %d, want 403", got)
	}
	if got := put(token, bytes.Repeat([]byte("x"), 2<<20)); got != 413 {
		t.Errorf("PUT over the size cap → %d, want 413", got)
	}
	if got := put(token, []byte("cfg")); got != 200 {
		t.Errorf("PUT with the issued token → %d, want 200", got)
	}
	if got := put(token, []byte("cfg-again")); got != 409 {
		t.Errorf("replayed PUT → %d, want 409", got)
	}
	// The operator can fetch the received file; a stranger's session cannot.
	e.operator("carol", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: e.customer("Customer C")})
	if r := e.call("alice", "GET", "/api/v1/uploads/"+slot.ID+"/file", nil); r.code != 200 || r.body != "cfg" {
		t.Errorf("alice fetch file → %d %q", r.code, r.body)
	}
	if r := e.call("carol", "GET", "/api/v1/uploads/"+slot.ID+"/file", nil); r.code != 404 {
		t.Errorf("carol fetch alice's file → %d, want 404", r.code)
	}
}

// TestIntegration_ServiceIdentity checks the narrowed internal service
// token (P1.4) against the live router.
func TestIntegration_ServiceIdentity(t *testing.T) {
	e := newTestEnv(t)
	devA := e.device("A001", nil)
	if r := e.call(testServiceToken, "GET", "/api/v1/devices/"+devA, nil); r.code != 200 {
		t.Errorf("service GET device → %d, want 200", r.code)
	}
	if r := e.call(testServiceToken, "GET", "/api/v1/devices", nil); r.code != 403 {
		t.Errorf("service GET device list → %d, want 403", r.code)
	}
	if r := e.call(testServiceToken, "POST", "/api/v1/auth/operators", map[string]string{"username": "x", "password": "y", "role": "noc"}); r.code != 403 {
		t.Errorf("service create operator → %d, want 403", r.code)
	}
	if r := e.call(testServiceToken, "POST", "/api/v1/auth/ticket", nil); r.code != 403 {
		t.Errorf("service mint browser ticket → %d, want 403", r.code)
	}
}
