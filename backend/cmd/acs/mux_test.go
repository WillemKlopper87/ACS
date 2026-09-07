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
