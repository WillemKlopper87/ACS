package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"acs/internal/config"
	"acs/internal/operators"
	"acs/internal/ratelimit"
)

const consoleOrigin = "http://console.test"

// cookieEnv serves the API the way cmd/api does for a browser: CORS for
// the console's origin, the origin check, then authentication.
type cookieEnv struct {
	*testEnv
	url string
}

func newCookieEnv(t *testing.T) *cookieEnv {
	t.Helper()
	savedLogin := loginLimiter
	loginLimiter = ratelimit.New(1000, 1000, time.Minute)
	t.Cleanup(func() { loginLimiter = savedLogin })
	e := newTestEnv(t)
	e.h.sessionCookies = true
	srv := httptest.NewServer(withCORS(consoleOrigin, withSessionOriginCheck(consoleOrigin, e.srv.Config.Handler)))
	t.Cleanup(srv.Close)
	e.operator("alice", operators.RoleSuperAdmin)
	return &cookieEnv{testEnv: e, url: srv.URL}
}

type browserCall struct {
	method, path string
	body         any
	cookie       *http.Cookie
	headers      map[string]string
}

func (e *cookieEnv) do(c browserCall) (*http.Response, string) {
	e.t.Helper()
	var rdr io.Reader
	if c.body != nil {
		b, _ := json.Marshal(c.body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(c.method, e.url+c.path, rdr)
	if c.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", c.method, c.path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

// signIn returns the session cookie a sign-in sets.
func (e *cookieEnv) signIn() *http.Cookie {
	e.t.Helper()
	res, body := e.do(browserCall{method: "POST", path: "/api/v1/auth/login", body: map[string]string{"username": "alice", "password": "pw-alice"},
		headers: map[string]string{"Origin": consoleOrigin}})
	if res.StatusCode != 200 {
		e.t.Fatalf("login = %d %s", res.StatusCode, body)
	}
	for _, c := range res.Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	e.t.Fatalf("login set no %s cookie; headers: %v", sessionCookieName, res.Header)
	return nil
}

func TestIntegration_SessionCookie(t *testing.T) {
	e := newCookieEnv(t)
	cookie := e.signIn()
	fromConsole := map[string]string{"Origin": consoleOrigin, csrfHeader: "1"}

	t.Run("the cookie cannot be read by scripts or sent from other sites", func(t *testing.T) {
		if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != sessionCookiePath {
			t.Fatalf("cookie attributes: HttpOnly=%v SameSite=%v Path=%q", cookie.HttpOnly, cookie.SameSite, cookie.Path)
		}
		if cookie.Secure {
			t.Fatal("a plain-HTTP lab sign-in set a Secure cookie, which the browser would drop")
		}
	})

	t.Run("it is the session: reads and changes work from the console", func(t *testing.T) {
		res, body := e.do(browserCall{method: "GET", path: "/api/v1/auth/session", cookie: cookie, headers: fromConsole})
		if res.StatusCode != 200 || !strings.Contains(body, `"username":"alice"`) || !strings.Contains(body, `"via":"cookie"`) {
			t.Fatalf("session = %d %s", res.StatusCode, body)
		}
		if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "true" || res.Header.Get("Access-Control-Allow-Origin") != consoleOrigin {
			t.Fatalf("CORS does not allow the console's credentialed request: %v", res.Header)
		}
		res, body = e.do(browserCall{method: "POST", path: "/api/v1/regions", body: map[string]string{"name": "cookie-region"}, cookie: cookie, headers: fromConsole})
		if res.StatusCode/100 != 2 {
			t.Fatalf("create with cookie and header = %d %s", res.StatusCode, body)
		}
	})

	t.Run("a forged request is refused", func(t *testing.T) {
		body := map[string]string{"name": "forged-region"}
		// Another site's form post: the browser would not even send a
		// SameSite=Strict cookie, but suppose it did. No custom header.
		res, _ := e.do(browserCall{method: "POST", path: "/api/v1/regions", body: body, cookie: cookie, headers: map[string]string{"Origin": "https://evil.example"}})
		if res.StatusCode != 403 {
			t.Fatalf("cross-origin without the header = %d, want 403", res.StatusCode)
		}
		// With the header but from another origin (it could only be added
		// after a preflight this API would not grant).
		res, _ = e.do(browserCall{method: "POST", path: "/api/v1/regions", body: body, cookie: cookie, headers: map[string]string{"Origin": "https://evil.example", csrfHeader: "1"}})
		if res.StatusCode != 403 {
			t.Fatalf("other origin with the header = %d, want 403", res.StatusCode)
		}
		// From the console's origin without the header.
		res, _ = e.do(browserCall{method: "POST", path: "/api/v1/regions", body: body, cookie: cookie, headers: map[string]string{"Origin": consoleOrigin}})
		if res.StatusCode != 403 {
			t.Fatalf("console origin without the header = %d, want 403", res.StatusCode)
		}
		var n int
		if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM regions WHERE name = 'forged-region'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("forged requests created %d regions (%v)", n, err)
		}
		// The preflight for the header is granted to the console's origin only.
		res, _ = e.do(browserCall{method: "OPTIONS", path: "/api/v1/regions", headers: map[string]string{"Origin": "https://evil.example", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": csrfHeader}})
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != consoleOrigin {
			t.Fatalf("preflight from another origin is answered with Allow-Origin %q", got)
		}
	})

	t.Run("the cookie never opens the console WebSocket or the web-GUI frame", func(t *testing.T) {
		dev := e.device("CK0001", nil)
		for _, path := range []string{"/api/v1/devices/" + dev + "/cli/connect?credential_id=x", "/api/v1/devices/" + dev + "/webgui/proxy/"} {
			res, body := e.do(browserCall{method: "GET", path: path, cookie: cookie, headers: fromConsole})
			if res.StatusCode != 401 || !strings.Contains(body, "ticket") {
				t.Fatalf("GET %s with the cookie = %d %s, want 401 asking for a ticket", path, res.StatusCode, body)
			}
		}
	})

	t.Run("a bearer token is not a cookie request", func(t *testing.T) {
		// Scripts and the BSS adapter are unaffected: no header, no Origin.
		if r := e.call("alice", "POST", "/api/v1/regions", map[string]string{"name": "bearer-region"}); r.code/100 != 2 {
			t.Fatalf("bearer create = %d %s", r.code, r.body)
		}
		// A request with both is judged as a bearer request.
		res, body := e.do(browserCall{method: "POST", path: "/api/v1/regions", body: map[string]string{"name": "both-region"}, cookie: cookie,
			headers: map[string]string{"Authorization": "Bearer " + e.tokens["alice"]}})
		if res.StatusCode/100 != 2 {
			t.Fatalf("bearer with a cookie present = %d %s", res.StatusCode, body)
		}
	})

	t.Run("signing out ends the session and removes the cookie", func(t *testing.T) {
		res, body := e.do(browserCall{method: "POST", path: "/api/v1/auth/logout", cookie: cookie, headers: fromConsole})
		if res.StatusCode != 204 {
			t.Fatalf("logout = %d %s", res.StatusCode, body)
		}
		removed := false
		for _, c := range res.Cookies() {
			if c.Name == sessionCookieName && c.MaxAge < 0 {
				removed = true
			}
		}
		if !removed {
			t.Fatalf("logout did not remove the cookie: %v", res.Header["Set-Cookie"])
		}
		// The old cookie value is dead on the server too.
		res, _ = e.do(browserCall{method: "GET", path: "/api/v1/auth/session", cookie: cookie, headers: fromConsole})
		if res.StatusCode != 401 {
			t.Fatalf("revoked cookie = %d, want 401", res.StatusCode)
		}
	})
}

func TestSessionCookieIsSecureOverHTTPSAndInProduction(t *testing.T) {
	cookieOf := func(h *handler, r *http.Request) *http.Cookie {
		w := httptest.NewRecorder()
		h.setSessionCookie(w, r, "tok", time.Now().Add(time.Hour))
		for _, c := range w.Result().Cookies() {
			if c.Name == sessionCookieName {
				return c
			}
		}
		return nil
	}
	plain := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	forwarded := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	forwarded.Header.Set("X-Forwarded-Proto", "https")

	lab := &handler{sessionCookies: true, deploymentProfile: config.ProfileLab}
	if c := cookieOf(lab, plain); c == nil || c.Secure {
		t.Fatalf("lab over HTTP: %+v, want a cookie that is not Secure", c)
	}
	if c := cookieOf(lab, forwarded); c == nil || !c.Secure {
		t.Fatalf("lab behind an HTTPS proxy: %+v, want Secure", c)
	}
	if c := cookieOf(&handler{sessionCookies: true, deploymentProfile: config.ProfileProduction}, plain); c == nil || !c.Secure {
		t.Fatalf("production: %+v, want Secure always", c)
	}
	if c := cookieOf(&handler{sessionCookies: false}, plain); c != nil {
		t.Fatalf("cookies off: got %+v", c)
	}
}

func TestSessionCookiesAreOffWhereTheyWouldBeUnsafeOrUseless(t *testing.T) {
	if !sessionCookiesAllowed("https://console.example", false) {
		t.Fatal("an exact console origin should get cookies")
	}
	if sessionCookiesAllowed("*", false) {
		t.Fatal(`cookies with ACS_API_CORS_ORIGIN="*"`)
	}
	// Device web pages on the API's own origin would ride the cookie.
	if sessionCookiesAllowed("https://console.example", true) {
		t.Fatal("cookies with ACS_WEBGUI_ALLOW_SAME_ORIGIN=true")
	}
}
