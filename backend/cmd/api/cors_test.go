package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestCORSAllowsEveryRegisteredMethod is the drift gate for withCORS's
// Access-Control-Allow-Methods (audit 2026-09-04 P0.1). The console is
// served from a different origin than the API in every shipped topology
// (frontend/nginx.conf serves static files and names the API in its CSP
// connect-src rather than proxying it), so a method missing from that
// header means the browser refuses the request at preflight — which is
// exactly how all 13 DELETE routes came to be unreachable from the UI
// while every server-side test passed.
//
// The required set is derived from routes.go rather than hardcoded, so
// registering a route with a new method fails here until CORS is updated.
func TestCORSAllowsEveryRegisteredMethod(t *testing.T) {
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}

	// Same extraction as TestOpenAPIMatchesRegisteredRoutes; only the
	// method is needed here.
	registered := map[string]bool{}
	re := regexp.MustCompile(`(?m)^\s*(?:route|routePerm)\("([A-Z]+)", "|mux\.Handle(?:Func)?\("([A-Z]+) `)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		switch {
		case m[1] != "":
			registered[m[1]] = true
		case m[2] != "":
			registered[m[2]] = true
		}
	}
	if len(registered) < 3 {
		t.Fatalf("only %d methods extracted from routes.go — the extraction regex is probably broken", len(registered))
	}

	rec := httptest.NewRecorder()
	withCORS("*", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/v1/devices", nil))

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight returned %d, want %d", rec.Code, http.StatusNoContent)
	}

	allowed := map[string]bool{}
	for _, m := range strings.Split(rec.Header().Get("Access-Control-Allow-Methods"), ",") {
		allowed[strings.TrimSpace(m)] = true
	}
	// The preflight itself is never a registered route, but a browser
	// still needs it advertised.
	if !allowed[http.MethodOptions] {
		t.Error("Access-Control-Allow-Methods omits OPTIONS")
	}

	var missing []string
	for m := range registered {
		if !allowed[m] {
			missing = append(missing, m)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("routes.go registers %s but Access-Control-Allow-Methods (%q) omits them — "+
			"every such route fails preflight from the console",
			strings.Join(missing, ", "), rec.Header().Get("Access-Control-Allow-Methods"))
	}
}
