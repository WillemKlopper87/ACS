package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestBSSOpenAPIMatchesRegisteredRoutes keeps the external /bss/v1 contract
// honest. TMF surfaces have their own versioned contracts; this test scopes
// itself to the adapter's custom BSS API, where a missing route otherwise
// becomes a runtime 404 for an OSS integration.
func TestBSSOpenAPIMatchesRegisteredRoutes(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile("../../openapi-bssadapter.yaml")
	if err != nil {
		t.Fatal(err)
	}

	registered := map[string]bool{}
	routeRE := regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) (/bss/v1/[^" ]+)"`)
	for _, match := range routeRE.FindAllStringSubmatch(string(source), -1) {
		registered[match[1]+" "+normalizeBSSPath(match[2])] = true
	}

	documented := map[string]bool{}
	var path string
	for _, line := range strings.Split(string(spec), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "  /bss/v1/") && strings.HasSuffix(line, ":") {
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		if path == "" || !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "     ") {
			continue
		}
		method := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(line), ":"))
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
			documented[method+" "+normalizeBSSPath(path)] = true
		}
	}

	var missing, stale []string
	for route := range registered {
		if !documented[route] {
			missing = append(missing, route)
		}
	}
	for route := range documented {
		if !registered[route] {
			stale = append(stale, route)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("registered BSS routes missing from OpenAPI:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("documented BSS routes not registered:\n  %s", strings.Join(stale, "\n  "))
	}
	if len(registered) < 6 {
		t.Fatalf("only %d BSS routes extracted; route matcher is likely broken", len(registered))
	}
}

func normalizeBSSPath(path string) string {
	return regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(path, "{}")
}
