package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestProblemCodesAreDeclared: every code the adapter writes is in
// problemTitles, and problemTitles is exactly the ProblemCode enum in the
// OpenAPI spec, so the code list integrators build on cannot drift.
func TestProblemCodesAreDeclared(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	used := regexp.MustCompile(`writeError\([^,]+,[^,]+,\s*"([A-Za-z_]+)"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range used.FindAllStringSubmatch(string(src), -1) {
			if _, ok := problemTitles[m[1]]; !ok {
				t.Errorf("%s writes undeclared problem code %s", f, m[1])
			}
		}
	}
	spec, err := os.ReadFile("../../openapi-bssadapter.yaml")
	if err != nil {
		t.Fatal(err)
	}
	enum := regexp.MustCompile(`(?s)ProblemCode:.*?enum: \[([^\]]*)\]`).FindSubmatch(spec)
	if enum == nil {
		t.Fatal("no ProblemCode enum in openapi-bssadapter.yaml")
	}
	declared := map[string]bool{}
	for _, c := range strings.Split(string(enum[1]), ",") {
		declared[strings.TrimSpace(c)] = true
	}
	for c := range problemTitles {
		if !declared[c] {
			t.Errorf("problem code %s is missing from the ProblemCode enum", c)
		}
	}
	if len(declared) != len(problemTitles) {
		t.Errorf("ProblemCode enum has %d codes, problemTitles %d", len(declared), len(problemTitles))
	}
}

func TestErrorsAreProblemDetails(t *testing.T) {
	h := withProblemMediaType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "ErrNotFound", "no such thing")
	}))
	for accept, want := range map[string]string{"application/problem+json": "application/problem+json", "": "application/json"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept", accept)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var p map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Header().Get("Content-Type") != want || p["type"] != "urn:acs:problem:v1:ErrNotFound" || p["status"] != float64(404) ||
			p["code"] != "ErrNotFound" || p["error"] != "ErrNotFound" || p["detail"] != "no such thing" || p["message"] != "no such thing" || p["title"] != "Not found" {
			t.Fatalf("Accept %q: %s %s", accept, rec.Header().Get("Content-Type"), rec.Body)
		}
	}
}
