package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestOpenAPIMatchesRegisteredRoutes is the drift gate between
// backend/openapi.yaml and the routes main.go actually registers (audit
// P2.5). It reads both as text — the route table is registered inline
// in main() via route()/routePerm()/mux.HandleFunc, and the spec is
// scanned for "  /path:" and "    method:" lines — so no YAML library
// or refactor of main() is needed. Every registered (method, path) must
// appear in the spec and vice versa.
func TestOpenAPIMatchesRegisteredRoutes(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}

	registered := map[string]bool{}
	re := regexp.MustCompile(`(?m)^\s*(?:route|routePerm)\("([A-Z]+)", "([^"]+)"|mux\.Handle(?:Func)?\("([A-Z]+) ([^"]+)"|mux\.HandleFunc\("(/[^" ]+)"`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		switch {
		case m[1] != "":
			registered[m[1]+" "+normalize(m[2])] = true
		case m[3] != "":
			registered[m[3]+" "+normalize(m[4])] = true
		case m[5] != "":
			// Method-less registration (the web-GUI proxy) — the spec
			// documents it as GET.
			registered["GET "+normalize(m[5])] = true
		}
	}
	// The metrics scrape endpoint is operational, not part of the
	// operator API contract.
	delete(registered, "GET /metrics")

	documented := map[string]bool{}
	var path string
	for _, line := range strings.Split(string(spec), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":") {
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		if path != "" && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") {
			method := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(line), ":"))
			switch method {
			case "GET", "POST", "PUT", "DELETE", "PATCH":
				documented[method+" "+normalize(path)] = true
			}
		}
	}

	var missing, stale []string
	for k := range registered {
		if !documented[k] {
			missing = append(missing, k)
		}
	}
	for k := range documented {
		if !registered[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("routes registered in main.go but absent from openapi.yaml:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("operations in openapi.yaml with no registered route:\n  %s", strings.Join(stale, "\n  "))
	}
	if len(registered) < 50 {
		t.Fatalf("only %d routes extracted from main.go — the extraction regex is probably broken", len(registered))
	}
}

// normalize maps every path-parameter spelling to "{}" so
// "/devices/{id}" and "/devices/{device_id}" compare equal, and strips
// Go 1.22's "{path...}" wildcard suffix marker.
func normalize(p string) string {
	p = strings.ReplaceAll(p, "...", "")
	return regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(p, "{}")
}
