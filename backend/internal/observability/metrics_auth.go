package observability

import (
	"crypto/subtle"
	"net/http"
	"os"
)

// MetricsTokenEnv names the setting that puts a credential on /metrics.
const MetricsTokenEnv = "ACS_METRICS_TOKEN"

// ProtectMetrics wraps a /metrics handler so that it answers only a
// scraper that presents the token as a bearer credential
// (docs/SECURITY-ASSESSMENT-2026-11-14.md, O9). /metrics shows route
// names, request volumes and error rates; it is served on the same
// listeners as the APIs, and the CWMP listener is reachable by every
// device network. With an empty token the handler is returned as it is,
// which is the behaviour from before: fine on a loopback listener, not on
// a published one.
//
// Prometheus sends the token with
//
//	authorization:
//	  credentials_file: /path/to/token
func ProtectMetrics(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "metrics need a bearer token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MetricsToken reads the configured token.
func MetricsToken() string { return os.Getenv(MetricsTokenEnv) }
