package main

import (
	"database/sql"
	"net/http"
	"os"
	"strings"

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
	//
	// Security review #56: production applies a final fail-closed boundary
	// before CWMP reaches the session handler. Plain HTTP, HTTP Basic and
	// the fleet-wide shared Digest username are lab/bootstrap compatibility
	// mechanisms, not production device identities. Established production
	// devices must arrive over TLS and authenticate with a per-device Digest
	// credential or a verified client certificate.
	mux.HandleFunc("/", metrics.InstrumentHTTP(cwmpRouteLabel, productionCWMPGuard(cwmp)))
	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("GET /healthz", observability.LivenessHandler())
	mux.Handle("GET /readyz", observability.ReadinessHandler(db))
	return mux
}

// productionCWMPGuard enforces the production side of the lab/production
// split without changing the compatibility behavior used by controlled
// hardware qualification. ACS_DEPLOYMENT_PROFILE defaults to lab elsewhere;
// only the exact value "production" activates these restrictions.
func productionCWMPGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("ACS_DEPLOYMENT_PROFILE")), "production") {
			if r.TLS == nil {
				http.Error(w, "CWMP TLS required in production", http.StatusUpgradeRequired)
				return
			}

			authorization := strings.TrimSpace(r.Header.Get("Authorization"))
			if hasAuthScheme(authorization, "Basic") {
				http.Error(w, "HTTP Basic is not permitted for production CWMP", http.StatusForbidden)
				return
			}

			// A fleet-wide username has no cryptographic binding to one ACS
			// device. Reject it outright in production so a leaked onboarding
			// secret cannot claim an existing tenant device. Controlled lab
			// onboarding can still use it while #56's bootstrap/graduation flow
			// is exercised, but production management requires a unique username
			// resolved through device_credentials (or mTLS).
			sharedUsername := strings.TrimSpace(os.Getenv("ACS_DIGEST_USERNAME"))
			if sharedUsername != "" && hasAuthScheme(authorization, "Digest") && digestAuthorizationUsername(authorization) == sharedUsername {
				http.Error(w, "shared CWMP credential is not permitted in production", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func hasAuthScheme(header, scheme string) bool {
	if header == "" {
		return false
	}
	parts := strings.SplitN(header, " ", 2)
	return len(parts) == 2 && strings.EqualFold(parts[0], scheme)
}

// digestAuthorizationUsername extracts only the Digest username directive.
// It is deliberately small and conservative: the real Digest parser and
// cryptographic verification still run in internal/auth; this helper exists
// only to reject use of the configured shared username at the production
// boundary before that compatibility credential can enter a CWMP session.
func digestAuthorizationUsername(header string) string {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Digest") {
		return ""
	}
	for _, directive := range strings.Split(parts[1], ",") {
		kv := strings.SplitN(strings.TrimSpace(directive), "=", 2)
		if len(kv) != 2 || !strings.EqualFold(strings.TrimSpace(kv[0]), "username") {
			continue
		}
		return strings.Trim(strings.TrimSpace(kv[1]), `"`)
	}
	return ""
}
