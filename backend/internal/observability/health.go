package observability

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

// Health endpoints (audit P1.5/P3.2). Liveness answers "is the process
// up" and never touches dependencies, so an orchestrator doesn't
// restart a healthy process during a database blip; readiness answers
// "should this instance receive traffic" and fails while Postgres is
// unreachable, so a load balancer drains it instead. Both are
// unauthenticated by design — they carry no data beyond a status word.
func LivenessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	})
}

func ReadinessHandler(db *sql.DB) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain")
		if err := db.PingContext(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("database unavailable"))
			return
		}
		w.Write([]byte("ready"))
	})
}
