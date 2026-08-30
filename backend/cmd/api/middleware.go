// HTTP middleware other than authentication (split out of main.go,
// audit P3.1); auth lives in auth_handlers.go.
package main

import (
	"net/http"
	"strings"
	"time"
)

// maxJSONBody caps request bodies on ordinary API routes (audit P1.2 /
// "request-body limits on JSON routes"). Nothing legitimate on these
// routes approaches this; the file-transfer routes below carry their
// own, larger, purpose-specific limits.
const (
	maxJSONBody   = 1 << 20  // 1 MiB
	maxImportBody = 32 << 20 // bulk device import (JSON/CSV/XML)
)

// withBodyLimit wraps every request body in a MaxBytesReader sized for
// its route class. Streaming/multipart routes are exempt because they
// bound themselves.
func withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(path, "/api/v1/uploads/") && strings.HasSuffix(path, "/receive"):
			// CPE upload receipt — bounded by ACS_UPLOAD_MAX_BYTES in the handler.
		case r.Method == http.MethodPost && path == "/api/v1/firmware/images":
			// multipart firmware publish — bounded by ParseMultipartForm.
		case r.Method == http.MethodPost && path == "/api/v1/devices/import":
			r.Body = http.MaxBytesReader(w, r.Body, maxImportBody)
		default:
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Rate limit defaults (build plan §7.4 sub-phase 7d — shipped after JWT
// auth, not before, per §7.3's own reasoning).
const (
	defaultAPIRateLimitPerSecond = 10
	defaultAPIRateLimitBurst     = 30
	rateLimitIdleTTL             = 10 * time.Minute
)

// withCORS lets the frontend dev server (a different origin — Vite on
// :5173, this API on :8080) call these endpoints from a browser. Runs
// outside withJWTAuth so a preflight OPTIONS never needs a token; origin
// restriction (ACS_API_CORS_ORIGIN) is the real boundary once that's set
// to something other than "*" — auth (withJWTAuth, added Phase 6) is what
// actually gates the requests themselves.
func withCORS(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
