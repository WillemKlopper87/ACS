package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"acs/internal/operators"
	"acs/internal/tenancy"
)

// TestIntegration_GrafanaGate: only a platform operator gets a ticket; the
// ticket becomes a session cookie at Grafana's origin; the proxy's check
// passes that cookie and nothing else, and stops passing it after logout.
func TestIntegration_GrafanaGate(t *testing.T) {
	e := newTestEnv(t)
	cust := e.customer("ISP A")
	e.operator("root", operators.RoleSuperAdmin)
	e.operator("noc-global", operators.RoleNOC)
	e.operator("noc-isp", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: cust})
	e.operator("viewer", operators.RoleReadOnly)
	for _, name := range []string{"noc-global", "viewer"} {
		op, err := e.h.operators.ByUsername(e.ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.h.operators.SetGlobalAccess(e.ctx, op.ID, true); err != nil {
			t.Fatal(err)
		}
	}

	if r := e.call("root", "POST", "/api/v1/grafana/ticket", nil); r.code != http.StatusNotFound {
		t.Fatalf("ticket with the gate off = %d %s", r.code, r.body)
	}
	e.h.grafanaGate, e.h.grafanaURL = true, "https://grafana.example.test"

	for name, want := range map[string]int{"root": 200, "noc-global": 200, "noc-isp": 403, "viewer": 403} {
		if r := e.call(name, "POST", "/api/v1/grafana/ticket", nil); r.code != want {
			t.Errorf("%s ticket = %d %s, want %d", name, r.code, r.body, want)
		}
	}

	r := e.call("noc-global", "POST", "/api/v1/grafana/ticket", nil)
	var out struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal([]byte(r.body), &out)
	if !strings.HasPrefix(out.URL, "https://grafana.example.test/acs-gate/enter?ticket=") {
		t.Fatalf("ticket url = %q", out.URL)
	}
	parsed, _ := url.Parse(out.URL)
	ticket := parsed.Query().Get("ticket")

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string, cookie *http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}

	// The session token in a URL, or a bad ticket, gets nothing.
	if res := get("/api/v1/grafana/enter?ticket="+url.QueryEscape(e.tokens["noc-global"]), nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("enter with a session token = %d", res.StatusCode)
	}
	if res := get("/api/v1/grafana/enter?ticket=garbage", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("enter with garbage = %d", res.StatusCode)
	}

	res := get("/api/v1/grafana/enter?ticket="+url.QueryEscape(ticket), nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("enter = %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	if again := get("/api/v1/grafana/enter?ticket="+url.QueryEscape(ticket), nil); again.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the same ticket a second time = %d, want 401", again.StatusCode)
	}
	var gate *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == grafanaGateCookie {
			gate = c
		}
	}
	if gate == nil || !gate.HttpOnly || !gate.Secure || gate.SameSite != http.SameSiteLaxMode || gate.MaxAge != 3600 {
		t.Fatalf("gate cookie = %+v", gate)
	}

	if res := get("/api/v1/grafana/check", gate); res.StatusCode != http.StatusNoContent {
		t.Fatalf("check with the cookie = %d", res.StatusCode)
	}
	if res := get("/api/v1/grafana/check", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("check without a cookie = %d", res.StatusCode)
	}
	// Tokens are not interchangeable: the ticket is no cookie, the cookie
	// is no ticket, and neither is a session token.
	if res := get("/api/v1/grafana/check", &http.Cookie{Name: grafanaGateCookie, Value: ticket}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("check with the ticket as cookie = %d", res.StatusCode)
	}
	if res := get("/api/v1/grafana/enter?ticket="+url.QueryEscape(gate.Value), nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("enter with the cookie as ticket = %d", res.StatusCode)
	}
	if r := e.call(gate.Value, "GET", "/api/v1/devices", nil); r.code != http.StatusUnauthorized {
		t.Fatalf("the gate cookie used as an API token = %d", r.code)
	}

	// Logging out of the console ends Grafana access at once.
	if r := e.call("noc-global", "POST", "/api/v1/auth/logout", nil); r.code >= 300 {
		t.Fatalf("logout = %d %s", r.code, r.body)
	}
	e.h.versionCache = nil
	if res := get("/api/v1/grafana/check", gate); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("check after logout = %d", res.StatusCode)
	}
}
