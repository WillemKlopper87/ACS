package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"acs/internal/observability"
)

// The CWMP gateway mounts a catch-all handler, and until this test existed
// it was the one service that never instrumented it: cmd/api and
// cmd/bssadapter both wrap their routes in metrics.InstrumentHTTP, but
// cmd/acs only exported fleet counters. That gap made
// acs_http_requests_total{service="acs"} a series that never existed,
// which in turn made the CWMPAuthFailures alert in infra/alert_rules.yml
// permanently unfirable — precisely the alert meant to catch a fleet-wide
// CPE credential mismatch.
func TestCWMPMuxRecordsRequestMetrics(t *testing.T) {
	metrics := observability.NewMetrics("acs")
	// A CPE that fails Digest auth is the case the alert cares about, so
	// the stub answers the way handleCWMP does for an unauthenticated POST.
	mux := newACSMux(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}, metrics, nil)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	mux.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, "acs_http_requests_total") {
		t.Fatalf("acs_http_requests_total absent from /metrics output:\n%s", body)
	}
	// The label set is what alert_rules.yml and the Grafana panels query.
	for _, want := range []string{`service="acs"`, `route="CWMP"`, `status="4xx"`, `method="POST"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected label %s in acs_http_requests_total; got:\n%s", want, body)
		}
	}
}

// The route label is a constant rather than r.URL.Path: the handler is a
// catch-all, so any scanner hitting random paths would otherwise create an
// unbounded number of Prometheus series.
func TestCWMPMuxUsesConstantRouteLabel(t *testing.T) {
	metrics := observability.NewMetrics("acs")
	mux := newACSMux(func(w http.ResponseWriter, r *http.Request) {}, metrics, nil)

	for _, path := range []string{"/", "/cwmp", "/acs", "/wp-login.php"} {
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, strings.NewReader("")))
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	for _, path := range []string{`route="/cwmp"`, `route="/acs"`, `route="/wp-login.php"`} {
		if strings.Contains(body, path) {
			t.Errorf("per-path route label %s leaked into metrics — cardinality is unbounded", path)
		}
	}
}

func TestProductionCWMPGuardRequiresTLS(t *testing.T) {
	t.Setenv("ACS_DEPLOYMENT_PROFILE", "production")
	called := false
	guard := productionCWMPGuard(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "http://acs.example/cwmp", strings.NewReader(""))
	rec := httptest.NewRecorder()
	guard(rec, req)

	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("plaintext production CWMP = %d, want %d", rec.Code, http.StatusUpgradeRequired)
	}
	if called {
		t.Fatal("plaintext production request reached CWMP session handler")
	}
}

func TestProductionCWMPGuardRejectsBasic(t *testing.T) {
	t.Setenv("ACS_DEPLOYMENT_PROFILE", "production")
	guard := productionCWMPGuard(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "https://acs.example/cwmp", strings.NewReader(""))
	req.Header.Set("Authorization", "Basic Y3BlOnNlY3JldA==")
	rec := httptest.NewRecorder()
	guard(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("Basic production CWMP = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestProductionCWMPGuardRejectsSharedDigestUsername(t *testing.T) {
	t.Setenv("ACS_DEPLOYMENT_PROFILE", "production")
	t.Setenv("ACS_DIGEST_USERNAME", "acs-device")
	called := false
	guard := productionCWMPGuard(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "https://acs.example/cwmp", strings.NewReader(""))
	req.Header.Set("Authorization", `Digest realm="acs", username="acs-device", nonce="n", uri="/cwmp", response="x"`)
	// Use a real HTTP header value rather than the Go-escaped form above.
	req.Header.Set("Authorization", strings.ReplaceAll(req.Header.Get("Authorization"), `\"`, `"`))
	rec := httptest.NewRecorder()
	guard(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("shared Digest production CWMP = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if called {
		t.Fatal("shared production credential reached CWMP session handler")
	}
}

func TestProductionCWMPGuardAllowsPerDeviceDigestForCryptographicVerification(t *testing.T) {
	t.Setenv("ACS_DEPLOYMENT_PROFILE", "production")
	t.Setenv("ACS_DIGEST_USERNAME", "acs-device")
	called := false
	guard := productionCWMPGuard(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusUnauthorized) // stand-in for the real Digest verifier
	})

	req := httptest.NewRequest(http.MethodPost, "https://acs.example/cwmp", strings.NewReader(""))
	req.Header.Set("Authorization", `Digest username="cpe-00e0fc-serial1", realm="acs", nonce="n", uri="/cwmp", response="x"`)
	req.Header.Set("Authorization", strings.ReplaceAll(req.Header.Get("Authorization"), `\"`, `"`))
	rec := httptest.NewRecorder()
	guard(rec, req)

	if !called {
		t.Fatal("per-device Digest credential did not reach the real verifier")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("per-device Digest stand-in response = %d, want 401", rec.Code)
	}
}

func TestLabCWMPGuardRetainsCompatibilityPath(t *testing.T) {
	t.Setenv("ACS_DEPLOYMENT_PROFILE", "lab")
	t.Setenv("ACS_DIGEST_USERNAME", "acs-device")
	called := false
	guard := productionCWMPGuard(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "http://acs.example/cwmp", strings.NewReader(""))
	req.Header.Set("Authorization", `Digest username="acs-device"`)
	rec := httptest.NewRecorder()
	guard(rec, req)

	if !called || rec.Code != http.StatusNoContent {
		t.Fatalf("lab compatibility path called=%v status=%d, want true/204", called, rec.Code)
	}
}

func TestDigestAuthorizationUsername(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   string
	}{
		{"normal", `Digest realm="acs", username="device-1", nonce="abc"`, "device-1"},
		{"mixed case", `dIgEsT Username="device-2", realm="acs"`, "device-2"},
		{"basic", `Basic YTpi`, ""},
		{"missing", `Digest realm="acs"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := strings.ReplaceAll(tc.header, `\"`, `"`)
			if got := digestAuthorizationUsername(header); got != tc.want {
				t.Fatalf("digestAuthorizationUsername(%q) = %q, want %q", header, got, tc.want)
			}
		})
	}
}
