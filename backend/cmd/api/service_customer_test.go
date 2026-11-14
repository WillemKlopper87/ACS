package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/operators"
	"acs/internal/ratelimit"
	"acs/internal/tenancy"
)

// The BSS adapter's service token used to reach every device. A call now
// names the customer it acts for and is confined to that customer's
// devices (docs/SECURITY-ASSESSMENT-2026-11-14.md, O5).
func TestIntegration_ServiceCallsAreConfinedToTheirCustomer(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("ISP A"), e.customer("ISP B")
	e.operator("root", operators.RoleSuperAdmin)
	devA, devB := e.device("SC0001A", &custA), e.device("SC0002B", &custB)

	service := func(method, path, acting string, body string) resp {
		t.Helper()
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
		req.Header.Set("Authorization", "Bearer "+testServiceToken)
		if acting != "" {
			req.Header.Set(actingCustomerHeader, acting)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return resp{r.StatusCode, string(b)}
	}
	write := `{"parameters":[{"name":"Device.WiFi.SSID.1.SSID","value":"x","type":"xsd:string"}]}`

	// Acting for A: A's device is reachable on every service route, B's
	// is not found on any of them, and nothing is queued for it.
	routes := []struct{ method, suffix, body string }{
		{"GET", "", ""},
		{"GET", "/parameter-names", ""},
		{"GET", "/wifi/networks", ""},
		{"GET", "/parameters?paths=Device.WiFi.SSID.1.SSID", ""},
		{"PUT", "/parameters", write},
	}
	var jobA string
	for _, rt := range routes {
		own := service(rt.method, "/api/v1/devices/"+devA+rt.suffix, custA, rt.body)
		if own.code/100 != 2 {
			t.Errorf("A's adapter, A's device: %s %s = %d %s", rt.method, rt.suffix, own.code, own.body)
		}
		if rt.method == "PUT" {
			if i := strings.Index(own.body, `"command_key":"`); i >= 0 {
				jobA = own.body[i+len(`"command_key":"`):]
				jobA = jobA[:strings.Index(jobA, `"`)]
			}
		}
		other := service(rt.method, "/api/v1/devices/"+devB+rt.suffix, custA, rt.body)
		if other.code != 404 {
			t.Errorf("A's adapter, B's device: %s %s = %d %s, want 404", rt.method, rt.suffix, other.code, other.body)
		}
	}
	var queuedForB int
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM jobs WHERE device_id = $1`, devB).Scan(&queuedForB); err != nil || queuedForB != 0 {
		t.Fatalf("jobs queued for B's device by A's adapter = %d (%v)", queuedForB, err)
	}

	// A job is its device's: B's adapter cannot read A's job.
	if jobA == "" {
		t.Fatal("no command key from A's write")
	}
	if r := service("GET", "/api/v1/jobs/"+jobA, custA, ""); r.code != 200 {
		t.Fatalf("A's adapter reads its job = %d %s", r.code, r.body)
	}
	if r := service("GET", "/api/v1/jobs/"+jobA, custB, ""); r.code != 404 {
		t.Fatalf("B's adapter reads A's job = %d, want 404", r.code)
	}

	// The header is a customer ID or nothing.
	if r := service("GET", "/api/v1/devices/"+devA, "everyone", ""); r.code != 400 {
		t.Fatalf("malformed acting customer = %d, want 400", r.code)
	}
	// The adapter reads named parameters only, never a whole cache.
	if r := service("GET", "/api/v1/devices/"+devA+"/parameters", custA, ""); r.code != 403 {
		t.Fatalf("whole parameter cache with the service token = %d, want 403", r.code)
	}

	// Without the header a call still reaches the fleet (lab, fleet-wide
	// clients) unless the deployment requires one.
	if r := service("GET", "/api/v1/devices/"+devB, "", ""); r.code != 200 {
		t.Fatalf("no acting customer, not required = %d", r.code)
	}
	serviceCallsRequireCustomer = true
	t.Cleanup(func() { serviceCallsRequireCustomer = false })
	if r := service("GET", "/api/v1/devices/"+devB, "", ""); r.code != 403 || !strings.Contains(r.body, actingCustomerHeader) {
		t.Fatalf("no acting customer, required = %d %s, want 403", r.code, r.body)
	}
	if r := service("GET", "/api/v1/devices/"+devA, custA, ""); r.code != 200 {
		t.Fatalf("named customer, required = %d", r.code)
	}

	// An operator cannot borrow the mechanism: the header means nothing on
	// an operator's own session.
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/devices/"+devB, nil)
	req.Header.Set("Authorization", "Bearer "+e.tokens["root"])
	req.Header.Set(actingCustomerHeader, custA)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("superadmin with the header = %d, want it ignored (200)", r.StatusCode)
	}
}

// A browser ticket is minted only for a device the operator may see
// (finding O4): otherwise it would be a way to get a credential naming
// another customer's device.
func TestIntegration_BrowserTicketNeedsADeviceInScope(t *testing.T) {
	e := newTestEnv(t)
	custA, custB := e.customer("ISP A"), e.customer("ISP B")
	devA, devB := e.device("BT0001A", &custA), e.device("BT0002B", &custB)
	e.operator("alice", operators.RoleManager, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: custA})
	if r := e.call("alice", "POST", "/api/v1/auth/ticket", map[string]string{"device_id": devA, "purpose": "cli"}); r.code != 200 {
		t.Fatalf("ticket for own device = %d %s", r.code, r.body)
	}
	if r := e.call("alice", "POST", "/api/v1/auth/ticket", map[string]string{"device_id": devB, "purpose": "cli"}); r.code != 404 {
		t.Fatalf("ticket for another customer's device = %d, want 404", r.code)
	}
	for _, body := range []map[string]string{{"device_id": devA}, {"purpose": "cli"}, {"device_id": devA, "purpose": "everything"}} {
		if r := e.call("alice", "POST", "/api/v1/auth/ticket", body); r.code != 400 {
			t.Fatalf("ticket with %v = %d, want 400", body, r.code)
		}
	}
}

// Operator passwords have a floor when they are set (finding O12), and an
// unknown username costs the same hash comparison as a wrong password
// (finding O11).
func TestIntegration_PasswordRulesAndSignInTiming(t *testing.T) {
	savedLogin := loginLimiter
	loginLimiter = ratelimit.New(1000, 1000, time.Minute)
	t.Cleanup(func() { loginLimiter = savedLogin })
	e := newTestEnv(t)
	e.operator("root", operators.RoleSuperAdmin)
	create := func(username, password string) resp {
		return e.call("root", "POST", "/api/v1/auth/operators", map[string]string{"username": username, "password": password, "role": "readonly"})
	}
	for password, why := range map[string]string{
		"short-pw-1":       "12 characters",
		"Password2026!":    "common word",
		"newoperator-9999": "username",
		"111111111111":     "repeated",
	} {
		if r := create("newoperator", password); r.code != 400 || !strings.Contains(r.body, why) {
			t.Errorf("password %q = %d %s, want 400 mentioning %q", password, r.code, r.body, why)
		}
	}
	if r := create("newoperator", "blue-Kettle-49-rain"); r.code/100 != 2 {
		t.Fatalf("a good password = %d %s", r.code, r.body)
	}

	start := time.Now()
	r := e.call("", "POST", "/api/v1/auth/login", map[string]string{"username": "nobody-by-this-name", "password": "whatever-it-is"})
	if r.code != 401 {
		t.Fatalf("unknown username = %d", r.code)
	}
	if d := time.Since(start); d < 5*time.Millisecond {
		t.Fatalf("unknown username answered in %v: no hash comparison was made, so timing tells it from a wrong password", d)
	}
}

// With the lookup configured, a password found in published breaches is
// refused when it is set, and a lookup that cannot be made does not stop
// a password change (finding O12).
func TestIntegration_BreachedPasswordIsRefused(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", operators.RoleSuperAdmin)
	const breached, clean = "blue-Kettle-49-rain", "green-Teapot-52-snow"
	suffix := func(p string) string {
		s := sha1.Sum([]byte(p))
		return strings.ToUpper(hex.EncodeToString(s[:]))[5:]
	}
	down := false
	hibp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, "%s:42\r\n", suffix(breached))
	}))
	defer hibp.Close()
	var err error
	if e.h.passwordBreaches, err = auth.NewBreachChecker(hibp.URL + "/range/"); err != nil {
		t.Fatal(err)
	}
	create := func(username, password string) resp {
		return e.call("root", "POST", "/api/v1/auth/operators", map[string]string{"username": username, "password": password, "role": "readonly"})
	}
	if r := create("op-breached", breached); r.code != 400 || !strings.Contains(r.body, "breaches") {
		t.Fatalf("a breached password = %d %s, want 400", r.code, r.body)
	}
	if r := create("op-clean", clean); r.code/100 != 2 {
		t.Fatalf("a password the service does not list = %d %s", r.code, r.body)
	}
	down = true
	if r := create("op-while-down", breached); r.code/100 != 2 {
		t.Fatalf("with the lookup service down = %d %s, want the password accepted on the other rules", r.code, r.body)
	}
}
