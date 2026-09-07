package main

import (
	"database/sql"
	"net/http"

	"acs/internal/observability"
)

// cwmpRouteLabel is the Prometheus "route" label for every CWMP request.
// It is deliberately a constant: the gateway is a catch-all, so labelling
// by r.URL.Path would let any internet scanner mint an unbounded number of
// series. Misconfigured device URLs stay visible in handleCWMP's own log
// line, which is the right place for a high-cardinality value.
const cwmpRouteLabel = "CWMP"

// newACSMux builds the CWMP gateway's HTTP surface. It exists as a
// separate constructor so the wiring — specifically that the CWMP handler
// is instrumented — is testable without standing up a database, a session
// store, or a listener.
func newACSMux(cwmp http.HandlerFunc, metrics *observability.Metrics, db *sql.DB) *http.ServeMux {
	mux := http.NewServeMux()
	// Catch-all rather than exact "/cwmp": CPEs in the field get provisioned
	// with ACS URLs like "http://host:7547/", "/cwmp/", or "/acs", and a 404
	// on the path mismatch looks to the operator exactly like "device won't
	// connect". Any POST that reaches this server is treated as CWMP;
	// handleCWMP logs the path so misconfigured device URLs stay visible.
	//
	// Wrapped in InstrumentHTTP so acs_http_requests_total{service="acs"}
	// exists at all: cmd/api and cmd/bssadapter have always instrumented
	// their routes, this one never did, which left the CWMPAuthFailures
	// alert querying a series that could not exist.
	mux.HandleFunc("/", metrics.InstrumentHTTP(cwmpRouteLabel, cwmp))
	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("GET /healthz", observability.LivenessHandler())
	mux.Handle("GET /readyz", observability.ReadinessHandler(db))
	return mux
}
