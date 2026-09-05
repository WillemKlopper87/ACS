package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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
		logger:           logger,
		devices:          devices.NewRepository(db),
		jobs:             jobs.NewRepository(db),
		params:           parameters.NewRepository(db),
		vendors:          adapters.NewRegistry(),
		auditor:          observability.NewAuditor(db),
		firmware:         firmware.NewRepository(db),
		firmwareFS:       fwFS,
		firmwareBase:     "http://acs.test",
		operators:        operators.NewRepository(db),
		jwtSecret:        testJWTSecret,
		transferKey:      transfer.DeriveKey(testJWTSecret),
		uploadMaxBytes:   1 << 20,
		firmwareMaxBytes: 1 << 20,
		metrics:          metrics,
		groups:           devices.NewGroupRepository(db),
		credentials:      credRepo,
		schedules:        scheduler.NewRepository(db),
		rollouts:         rollout.NewRepository(db),
		policies:         policy.NewRepository(db),
		uploads:          uploads.NewRepository(db),
		uploadsFS:        upFS,
		uploadsBase:      "http://acs.test",
		templates:        templates.NewRepository(db),
		cli:              cliRepo,
		permissions:      operators.NewPermissionRepository(db),
		mailer:           mailer.New(mailer.Config{}, logger),
		frontendBaseURL:  "http://console.test",
		tenancy:          tenancy.NewRepository(db),
		dashboards:       dashboard.NewRepository(db),
		bssMappings:      bss.NewRepository(db),
		bssWebhooks:      bss.NewWebhookRepository(db),
		bssOAuthClients:  bss.NewOAuthRepository(db),
		bssHTTPClient:    &http.Client{Timeout: time.Second},
		vpnPeers:         vpnRepo,
	}
	mux := h.registerRoutes(metrics, db)
	srv := httptest.NewServer(withJWTAuth(testJWTSecret, testServiceToken, h.tokenCurrent, withBodyLimit(mux)))
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
	tok, _ := auth.SignJWT(testJWTSecret, auth.Claims{Subject: username, Role: role, IssuedAt: now, ExpiresAt: now.Add(time.Hour), Version: op.TokenVersion})
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

// TestIntegration_H3SubResourceIsolation covers the H-3/P2.1 IDOR gaps
// that survived the original P0.2 device-addressed scoping pass: cross-
// device credential activate/revoke, CLI-credential delete with no
// device in the check at all, cross-tenant bulk import, and fleet-wide
// audit-log/VPN-peer reads for a scoped operator.
func TestIntegration_H3SubResourceIsolation(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("Customer A"), e.customer("Customer B")
	devA := e.device("A001", &custA)
	devB := e.device("B001", &custB)
	e.operator("alice", operators.RoleManager, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.operator("bob", operators.RoleManager, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custB})
	e.grant(operators.RoleManager, operators.PermCredentialManage, operators.PermCLIAccess, operators.PermTenancyManage)

	t.Run("credential activate/revoke cannot cross devices", func(t *testing.T) {
		r := e.call("bob", "POST", "/api/v1/devices/"+devB+"/credentials/rotate", nil)
		if r.code != 200 && r.code != 202 {
			t.Fatalf("bob rotate own credential → %d %s", r.code, r.body)
		}
		var created struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(r.body), &created)
		if created.ID == "" {
			t.Fatalf("rotate response has no credential id: %s", r.body)
		}
		// alice pairs her own device_id with bob's credential_id.
		if r := e.call("alice", "POST", "/api/v1/devices/"+devA+"/credentials/"+created.ID+"/activate", nil); r.code != 404 {
			t.Errorf("alice activate bob's credential via her own device path → %d, want 404", r.code)
		}
		if r := e.call("alice", "POST", "/api/v1/devices/"+devA+"/credentials/"+created.ID+"/revoke", nil); r.code != 404 {
			t.Errorf("alice revoke bob's credential via her own device path → %d, want 404", r.code)
		}
		// bob himself still can (sanity check the fix didn't over-block).
		if r := e.call("bob", "POST", "/api/v1/devices/"+devB+"/credentials/"+created.ID+"/revoke", nil); r.code != 200 {
			t.Errorf("bob revoke his own credential → %d %s, want 200", r.code, r.body)
		}
	})

	t.Run("cli credential delete is scoped", func(t *testing.T) {
		r := e.call("bob", "POST", "/api/v1/devices/"+devB+"/cli/credentials", map[string]any{
			"protocol": "SSH", "host": "10.99.0.5", "port": 22, "username": "root", "password": "x",
		})
		if r.code != 200 && r.code != 201 {
			t.Fatalf("bob create cli credential → %d %s", r.code, r.body)
		}
		var created struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(r.body), &created)
		if created.ID == "" {
			t.Fatalf("cli credential response has no id: %s", r.body)
		}
		if r := e.call("alice", "DELETE", "/api/v1/devices/"+devA+"/cli/credentials/"+created.ID, nil); r.code != 404 {
			t.Errorf("alice delete bob's cli credential → %d, want 404", r.code)
		}
		if r := e.call("bob", "DELETE", "/api/v1/devices/"+devB+"/cli/credentials/"+created.ID, nil); r.code != 204 {
			t.Errorf("bob delete his own cli credential → %d, want 204", r.code)
		}
	})

	t.Run("bulk import cannot plant or reassign a device outside scope", func(t *testing.T) {
		r := e.call("alice", "POST", "/api/v1/devices/import?format=json", []map[string]any{
			{"manufacturer": "Evil", "oui": "AABBCC", "serial_number": "HIJACK01", "customer_id": custB},
		})
		if r.code != 200 {
			t.Fatalf("import → %d %s", r.code, r.body)
		}
		if !strings.Contains(r.body, `"status":"error"`) || strings.Contains(r.body, `"succeeded":1`) {
			t.Errorf("import into a foreign customer_id should be rejected, got: %s", r.body)
		}

		// bob imports a device into his own customer...
		importRow := map[string]any{"manufacturer": "Importable", "oui": "DDEEFF", "serial_number": "SHARED01", "customer_id": custB}
		r = e.call("bob", "POST", "/api/v1/devices/import?format=json", []map[string]any{importRow})
		if r.code != 200 || !strings.Contains(r.body, `"succeeded":1`) {
			t.Fatalf("bob import own device → %d %s", r.code, r.body)
		}
		// ...and alice re-importing the exact same natural key, scoped to
		// her own customer, must not reassign it away from bob's.
		hijack := map[string]any{"manufacturer": "Importable", "oui": "DDEEFF", "serial_number": "SHARED01", "customer_id": custA}
		r = e.call("alice", "POST", "/api/v1/devices/import?format=json", []map[string]any{hijack})
		if r.code != 200 {
			t.Fatalf("import → %d %s", r.code, r.body)
		}
		if !strings.Contains(r.body, `"status":"error"`) {
			t.Errorf("re-importing a foreign tenant's existing device should be rejected, got: %s", r.body)
		}
		imported, err := e.h.devices.GetByOUIserial(e.ctx, "DDEEFF++SHARED01")
		if err != nil {
			t.Fatal(err)
		}
		if imported.CustomerID == nil || *imported.CustomerID != custB {
			t.Errorf("shared device's customer_id after hijack attempt = %v, want unchanged (%s)", imported.CustomerID, custB)
		}
	})

	t.Run("audit log is scoped", func(t *testing.T) {
		if err := e.h.auditor.Record(e.ctx, "system", devB, "TestEvent", map[string]any{"secret": "bob-only"}); err != nil {
			t.Fatal(err)
		}
		r := e.call("alice", "GET", "/api/v1/audit-log", nil)
		if r.code != 200 {
			t.Fatalf("alice list audit log → %d", r.code)
		}
		if strings.Contains(r.body, "bob-only") || strings.Contains(r.body, devB) {
			t.Errorf("alice's audit log leaks bob's device entry: %s", r.body)
		}
		r = e.call("bob", "GET", "/api/v1/audit-log", nil)
		if !strings.Contains(r.body, devB) {
			t.Errorf("bob's own audit log should include his device's entries: %s", r.body)
		}
	})

	t.Run("vpn peer list is scoped", func(t *testing.T) {
		r := e.call("alice", "GET", "/api/v1/vpn/peers", nil)
		if r.code != 200 {
			t.Fatalf("alice list vpn peers → %d %s", r.code, r.body)
		}
		if strings.Contains(r.body, devB) {
			t.Errorf("alice's vpn peer list leaks bob's device: %s", r.body)
		}
	})

	t.Run("vpn peer revoke is scoped", func(t *testing.T) {
		r := e.call("bob", "POST", "/api/v1/devices/"+devB+"/vpn/enroll", nil)
		if r.code != 200 && r.code != 201 {
			t.Fatalf("bob enroll vpn peer → %d %s", r.code, r.body)
		}
		var created struct {
			Peer struct {
				ID string `json:"id"`
			} `json:"peer"`
		}
		_ = json.Unmarshal([]byte(r.body), &created)
		if created.Peer.ID == "" {
			t.Fatalf("enroll response has no peer id: %s", r.body)
		}
		if r := e.call("alice", "DELETE", "/api/v1/vpn/peers/"+created.Peer.ID, nil); r.code != 404 {
			t.Errorf("alice revoke bob's vpn peer → %d, want 404", r.code)
		}
		if r := e.call("bob", "DELETE", "/api/v1/vpn/peers/"+created.Peer.ID, nil); r.code != 204 {
			t.Errorf("bob revoke his own vpn peer → %d, want 204", r.code)
		}
	})
}

// TestIntegration_ZeroScopeDenyByDefault is the P0.1 acceptance gate
// (ACS_REMEDIATION_EXECUTION_PROTOCOL_2026-09-03.md §5): zero
// operator_scopes rows must mean zero device access, never unrestricted
// access, for any non-superadmin operator — and unrestricted access
// requires the explicit GlobalAccess grant, never an inferred one.
func TestIntegration_ZeroScopeDenyByDefault(t *testing.T) {
	e := newTestEnv(t)
	custA := e.customer("Customer A")
	devA := e.device("A001", &custA)
	e.operator("root", operators.RoleSuperAdmin)
	e.grant(operators.RoleManager, operators.PermDevicesWrite)
	e.grant(operators.RoleNOC, operators.PermDevicesWrite)

	t.Run("new manager has no devices until scope assigned", func(t *testing.T) {
		e.operator("mallory", operators.RoleManager) // no scopes passed
		if r := e.call("mallory", "GET", "/api/v1/devices", nil); !strings.Contains(r.body, `"total":0`) {
			t.Errorf("scopeless new manager's device list = %s, want total:0", r.body)
		}
		if r := e.call("mallory", "GET", "/api/v1/devices/"+devA, nil); r.code != 404 {
			t.Errorf("scopeless new manager GET device → %d, want 404", r.code)
		}
	})

	t.Run("new noc has no devices until scope assigned", func(t *testing.T) {
		e.operator("nancy", operators.RoleNOC) // no scopes passed
		if r := e.call("nancy", "GET", "/api/v1/devices", nil); !strings.Contains(r.body, `"total":0`) {
			t.Errorf("scopeless new noc's device list = %s, want total:0", r.body)
		}
		if r := e.call("nancy", "GET", "/api/v1/devices/"+devA, nil); r.code != 404 {
			t.Errorf("scopeless new noc GET device → %d, want 404", r.code)
		}
	})

	t.Run("removing last scope does not grant global access", func(t *testing.T) {
		e.operator("oscar", operators.RoleManager, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
		if r := e.call("oscar", "GET", "/api/v1/devices/"+devA, nil); r.code != 200 {
			t.Fatalf("oscar with a customer scope GET own device → %d, want 200", r.code)
		}
		op, err := e.h.operators.ByUsername(e.ctx, "oscar")
		if err != nil {
			t.Fatal(err)
		}
		if err := e.h.tenancy.SetOperatorScopes(e.ctx, op.ID, nil); err != nil {
			t.Fatalf("clear scopes: %v", err)
		}
		if r := e.call("oscar", "GET", "/api/v1/devices/"+devA, nil); r.code != 404 {
			t.Errorf("oscar after last scope removed GET device → %d, want 404 (must not fall back to unrestricted)", r.code)
		}
		if r := e.call("oscar", "GET", "/api/v1/devices", nil); !strings.Contains(r.body, `"total":0`) {
			t.Errorf("oscar after last scope removed device list = %s, want total:0", r.body)
		}
	})

	t.Run("explicit global operator can access all customers", func(t *testing.T) {
		e.operator("gina", operators.RoleManager) // no scopes
		op, err := e.h.operators.ByUsername(e.ctx, "gina")
		if err != nil {
			t.Fatal(err)
		}
		if r := e.call("gina", "GET", "/api/v1/devices/"+devA, nil); r.code != 404 {
			t.Fatalf("gina before global grant GET device → %d, want 404", r.code)
		}
		if err := e.h.operators.SetGlobalAccess(e.ctx, op.ID, true); err != nil {
			t.Fatalf("grant global access: %v", err)
		}
		if r := e.call("gina", "GET", "/api/v1/devices/"+devA, nil); r.code != 200 {
			t.Errorf("gina after global grant GET device → %d, want 200", r.code)
		}
		if r := e.call("gina", "GET", "/api/v1/devices", nil); !strings.Contains(r.body, `"total":1`) {
			t.Errorf("gina after global grant device list = %s, want total:1", r.body)
		}
	})

	t.Run("superadmin remains global regardless of scope rows", func(t *testing.T) {
		if r := e.call("root", "GET", "/api/v1/devices/"+devA, nil); r.code != 200 {
			t.Errorf("root (superadmin) GET device → %d, want 200", r.code)
		}
		if r := e.call("root", "GET", "/api/v1/devices", nil); !strings.Contains(r.body, `"total":1`) {
			t.Errorf("root (superadmin) device list = %s, want total:1", r.body)
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

// TestIntegration_ConcurrentUploadPUTs is the P1.3/M-13 acceptance gate:
// two genuinely simultaneous PUTs against the same valid upload slot
// must leave exactly one winner, one 409 conflict, and a retained object
// whose bytes and recorded sha256 both belong wholly to the winner —
// never an interleaved mix of both bodies, and never a file the loser's
// cleanup deleted out from under the winner.
func TestIntegration_ConcurrentUploadPUTs(t *testing.T) {
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
	token := payload.URL[strings.Index(payload.URL, "?token=")+len("?token="):]

	bodyA := bytes.Repeat([]byte("A"), 200000)
	bodyB := bytes.Repeat([]byte("B"), 200000)
	put := func(body []byte) (int, error) {
		url := e.srv.URL + "/api/v1/uploads/" + slot.ID + "/receive?token=" + token
		req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer res.Body.Close()
		return res.StatusCode, nil
	}

	var wg sync.WaitGroup
	codes := make([]int, 2)
	wg.Add(2)
	go func() { defer wg.Done(); codes[0], _ = put(bodyA) }()
	go func() { defer wg.Done(); codes[1], _ = put(bodyB) }()
	wg.Wait()

	won200, lost409 := 0, 0
	for _, c := range codes {
		switch c {
		case 200:
			won200++
		case 409:
			lost409++
		}
	}
	if won200 != 1 || lost409 != 1 {
		t.Fatalf("concurrent PUT status codes = %v, want exactly one 200 and one 409", codes)
	}

	rGet := e.call("alice", "GET", "/api/v1/uploads/"+slot.ID+"/file", nil)
	if rGet.code != 200 {
		t.Fatalf("fetch retained file → %d", rGet.code)
	}
	body := []byte(rGet.body)
	if !bytes.Equal(body, bodyA) && !bytes.Equal(body, bodyB) {
		t.Fatalf("retained file is %d bytes and belongs to neither writer wholly (interleaved/corrupted) — first 20 bytes: %q", len(body), body[:min(20, len(body))])
	}

	f, err := e.h.uploads.ByID(e.ctx, slot.ID)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	wantSHA := hex.EncodeToString(sum[:])
	if f.SHA256 == nil || *f.SHA256 != wantSHA {
		t.Errorf("recorded sha256 = %v, want %s (the actual retained file's hash)", f.SHA256, wantSHA)
	}
}

// TestIntegration_BrowserTicketTokenVersion is the P1.5 acceptance gate:
// a browser ticket must carry the operator's token version at mint time,
// not the zero value — so it dies with the same revocation event as its
// parent session, and a ticket minted from a fresh session after
// logout/password-reset works again.
func TestIntegration_BrowserTicketTokenVersion(t *testing.T) {
	e := newTestEnv(t)
	custA := e.customer("Customer A")
	devA := e.device("A001", &custA)
	e.operator("alice", operators.RoleManager, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	e.grant(operators.RoleManager, operators.PermCLIAccess)

	mintTicket := func() string {
		t.Helper()
		r := e.call("alice", "POST", "/api/v1/auth/ticket", nil)
		if r.code != 200 {
			t.Fatalf("mint ticket → %d %s", r.code, r.body)
		}
		var out struct {
			Ticket string `json:"ticket"`
		}
		_ = json.Unmarshal([]byte(r.body), &out)
		return out.Ticket
	}
	// A ticket-protected route (webgui proxy) against a real, in-scope
	// device with no webgui configured: 401 means withJWTAuth's version
	// check itself rejected the ticket; 404 ("no web GUI configured")
	// means it passed that check and reached the real handler — the
	// distinction this test needs, without standing up a full proxy target.
	useTicket := func(ticket string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/devices/"+devA+"/webgui/proxy/x?token="+ticket, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	relogin := func() {
		t.Helper()
		r := e.call("", "POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": "pw-alice"})
		if r.code != 200 {
			t.Fatalf("relogin → %d %s", r.code, r.body)
		}
		var lr struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal([]byte(r.body), &lr)
		e.tokens["alice"] = lr.Token
	}

	t.Run("uses current token version", func(t *testing.T) {
		if got := useTicket(mintTicket()); got == http.StatusUnauthorized {
			t.Errorf("freshly minted ticket → 401, want to pass the auth layer (got %d)", got)
		}
	})

	t.Run("works after logout and relogin", func(t *testing.T) {
		stale := mintTicket()
		if r := e.call("alice", "POST", "/api/v1/auth/logout", nil); r.code != 204 {
			t.Fatalf("logout → %d %s", r.code, r.body)
		}
		if got := useTicket(stale); got != http.StatusUnauthorized {
			t.Errorf("ticket minted before logout → %d, want 401", got)
		}
		relogin()
		if got := useTicket(mintTicket()); got == http.StatusUnauthorized {
			t.Errorf("ticket minted after relogin → 401, want to pass the auth layer (got %d)", got)
		}
	})

	t.Run("works after password reset and relogin", func(t *testing.T) {
		stale := mintTicket()
		op, err := e.h.operators.ByUsername(e.ctx, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if err := e.h.operators.UpdatePassword(e.ctx, op.ID, op.PasswordHash); err != nil {
			t.Fatal(err)
		}
		e.h.forgetTokenVersion("alice")
		if got := useTicket(stale); got != http.StatusUnauthorized {
			t.Errorf("ticket minted before password reset → %d, want 401", got)
		}
		relogin()
		if got := useTicket(mintTicket()); got == http.StatusUnauthorized {
			t.Errorf("ticket minted after post-reset relogin → 401, want to pass the auth layer (got %d)", got)
		}
	})
}

// TestIntegration_FirmwareUploadSizeLimit is the P1.4 acceptance gate:
// ParseMultipartForm's own argument is a memory-buffer threshold, not a
// total request-size limit, so the publish endpoint must enforce
// firmwareMaxBytes itself.
func TestIntegration_FirmwareUploadSizeLimit(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", operators.RoleSuperAdmin)

	upload := func(fileSize int, version string) int {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("vendor", "TestVendor")
		_ = mw.WriteField("model", "TestModel")
		_ = mw.WriteField("version", version)
		fw, err := mw.CreateFormFile("file", "fw.bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(bytes.Repeat([]byte("x"), fileSize)); err != nil {
			t.Fatal(err)
		}
		if err := mw.Close(); err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/v1/firmware/images", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+e.tokens["root"])
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}

	if got := upload(1<<20+1024, "v1-too-big"); got != http.StatusRequestEntityTooLarge {
		t.Errorf("upload over firmwareMaxBytes → %d, want 413", got)
	}
	if got := upload(1024, "v1-ok"); got != http.StatusCreated {
		t.Errorf("upload under firmwareMaxBytes → %d, want 201", got)
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

// TestIntegration_SessionRevocation: logout and password change must
// invalidate outstanding JWTs; login is throttled per username+IP.
func TestIntegration_SessionRevocation(t *testing.T) {
	e := newTestEnv(t)
	e.operator("alice", operators.RoleReadOnly)
	if r := e.call("alice", "GET", "/api/v1/devices", nil); r.code != 200 {
		t.Fatalf("pre-logout → %d", r.code)
	}
	if r := e.call("alice", "POST", "/api/v1/auth/logout", nil); r.code != 204 {
		t.Fatalf("logout → %d %s", r.code, r.body)
	}
	if r := e.call("alice", "GET", "/api/v1/devices", nil); r.code != 401 {
		t.Errorf("token after logout → %d, want 401", r.code)
	}

	// Fresh login works, then a password change kills that session too.
	login := func(user, pass string) resp {
		return e.call("", "POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pass})
	}
	r := login("alice", "pw-alice")
	if r.code != 200 {
		t.Fatalf("login → %d %s", r.code, r.body)
	}
	var lr struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(r.body), &lr)
	if got := e.call(lr.Token, "GET", "/api/v1/devices", nil); got.code != 200 {
		t.Fatalf("fresh token → %d", got.code)
	}
	op, _ := e.h.operators.ByUsername(e.ctx, "alice")
	if err := e.h.operators.UpdatePassword(e.ctx, op.ID, op.PasswordHash); err != nil {
		t.Fatal(err)
	}
	e.h.forgetTokenVersion("alice")
	if got := e.call(lr.Token, "GET", "/api/v1/devices", nil); got.code != 401 {
		t.Errorf("token after password change → %d, want 401", got.code)
	}

	// Brute force: after the burst, wrong passwords are throttled (429),
	// and the throttle is per username — a different account is unaffected.
	e.operator("bob", operators.RoleReadOnly)
	last := 0
	for i := 0; i < 8; i++ {
		last = login("alice", "wrong").code
	}
	if last != 429 {
		t.Errorf("8th bad login → %d, want 429", last)
	}
	if got := login("bob", "pw-bob"); got.code != 200 {
		t.Errorf("bob's login during alice's throttle → %d, want 200", got.code)
	}
}

// Offboarding (UI/UX review 2026-09-04 P1.4). An operator who leaves has
// to stop being able to act immediately, without deleting the row their
// audit_log entries point at — and the mechanism must not be usable to
// lock the deployment out of its own admin plane.
func TestIntegration_OperatorDisable(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", operators.RoleAdmin)
	e.operator("leaver", operators.RoleReadOnly)

	login := func(user, pass string) resp {
		return e.call("", "POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pass})
	}
	leaver, err := e.h.operators.ByUsername(e.ctx, "leaver")
	if err != nil {
		t.Fatal(err)
	}

	// A live session, established before the account is disabled.
	r := login("leaver", "pw-leaver")
	if r.code != 200 {
		t.Fatalf("login → %d %s", r.code, r.body)
	}
	var lr struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(r.body), &lr)
	if got := e.call(lr.Token, "GET", "/api/v1/devices", nil); got.code != 200 {
		t.Fatalf("pre-disable → %d", got.code)
	}

	path := "/api/v1/auth/operators/" + leaver.ID + "/disabled"
	if got := e.call("root", "PUT", path, map[string]bool{"disabled": true}); got.code != 204 {
		t.Fatalf("disable → %d %s", got.code, got.body)
	}

	// The session they already held is dead, not merely un-renewable.
	if got := e.call(lr.Token, "GET", "/api/v1/devices", nil); got.code != 401 {
		t.Errorf("existing session after disable → %d, want 401", got.code)
	}
	// And they cannot get a new one — with the same message a wrong
	// password gets, so this can't be used to probe account state.
	if got := login("leaver", "pw-leaver"); got.code != 401 {
		t.Errorf("login while disabled → %d, want 401", got.code)
	}

	// The row survives, so audit attribution still resolves.
	if _, err := e.h.operators.ByUsername(e.ctx, "leaver"); err != nil {
		t.Errorf("disabled operator row should still exist: %v", err)
	}

	// Re-enabling restores login.
	if got := e.call("root", "PUT", path, map[string]bool{"disabled": false}); got.code != 204 {
		t.Fatalf("re-enable → %d %s", got.code, got.body)
	}
	if got := login("leaver", "pw-leaver"); got.code != 200 {
		t.Errorf("login after re-enable → %d, want 200", got.code)
	}
}

// Disabling must not be usable to lock the deployment out of its own
// admin plane. Note the interaction between the two guards: because the
// caller is themselves an active superadmin, any *other* superadmin they
// target leaves at least two active, so the API path can only ever reach
// the self-disable refusal. The last-active-superadmin check behind it is
// deliberate defence in depth for a non-API caller (a future CLI or
// migration touching the same repository method), and is exercised at
// that level below rather than pretended to be reachable from here.
func TestIntegration_OperatorDisableLockoutGuards(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", operators.RoleAdmin)
	root, err := e.h.operators.ByUsername(e.ctx, "root")
	if err != nil {
		t.Fatal(err)
	}

	self := "/api/v1/auth/operators/" + root.ID + "/disabled"
	if got := e.call("root", "PUT", self, map[string]bool{"disabled": true}); got.code != 409 {
		t.Errorf("disabling your own account → %d, want 409", got.code)
	}
	// Refused means refused: the account still works.
	if got := e.call("root", "GET", "/api/v1/devices", nil); got.code != 200 {
		t.Errorf("root after a refused self-disable → %d, want 200", got.code)
	}

	// A peer superadmin can be disabled, since others remain active.
	e.operator("root2", operators.RoleAdmin)
	root2, err := e.h.operators.ByUsername(e.ctx, "root2")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.call("root", "PUT", "/api/v1/auth/operators/"+root2.ID+"/disabled", map[string]bool{"disabled": true}); got.code != 204 {
		t.Fatalf("disabling a peer superadmin → %d %s", got.code, got.body)
	}

	// The count the guard reads must now see exactly the one remaining
	// active superadmin — this is what makes the check meaningful for any
	// caller that isn't already holding an admin session.
	n, err := e.h.operators.CountActiveAdmins(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("active superadmins after disabling root2 = %d, want 1", n)
	}
}

// Only a superadmin may take an operator out of service.
func TestIntegration_OperatorDisableRequiresAdmin(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", operators.RoleAdmin)
	e.operator("manager", operators.RoleManager)
	e.operator("victim", operators.RoleReadOnly)
	victim, err := e.h.operators.ByUsername(e.ctx, "victim")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/auth/operators/" + victim.ID + "/disabled"
	if got := e.call("manager", "PUT", path, map[string]bool{"disabled": true}); got.code != 403 {
		t.Errorf("manager disabling an operator → %d, want 403", got.code)
	}
}

// A schedule fires unattended and on repeat, so createScheduledJob must
// refuse a job_type the worker cannot dispatch. The worker's switch
// already declined to run one, so nothing unlisted was ever executed —
// but accepting it stored a schedule that reads as enabled in the
// console, never fires, and logs an error on every tick forever.
func TestIntegration_ScheduledJobTypeValidation(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", operators.RoleAdmin)
	dev := e.device("AABBCC-SCHED-1", nil)

	body := func(jobType string) map[string]any {
		return map[string]any{
			"name": "probe", "job_type": jobType,
			"target_type": "DEVICE", "target_id": dev,
			"payload":          map[string]any{"paths": []string{"Device.DeviceInfo.SoftwareVersion"}},
			"interval_seconds": 300,
		}
	}

	if got := e.call("root", "POST", "/api/v1/scheduled-jobs", body("GET_PARAMETER")); got.code != 201 {
		t.Fatalf("a schedulable job_type → %d %s, want 201", got.code, got.body)
	}
	// Destructive one-shots are the ones that matter here.
	for _, jobType := range []string{"FACTORY_RESET", "REBOOT", "FIRMWARE_DOWNLOAD", "NOT_A_JOB_TYPE"} {
		got := e.call("root", "POST", "/api/v1/scheduled-jobs", body(jobType))
		if got.code != 400 {
			t.Errorf("job_type %s → %d, want 400 (%s)", jobType, got.code, got.body)
		}
	}
}
