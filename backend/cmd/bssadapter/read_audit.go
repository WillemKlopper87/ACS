package main

import (
	"context"
	"net/http"
	"sync"
	"time"

	"acs/internal/observability"
)

// The adapter audited what an integration changed, not what it read or
// when it obtained a token (docs/SECURITY-ASSESSMENT-2026-11-14.md, O13).
// After an incident it was not possible to say which client had read
// which customer's devices and subscribers.
//
// Token issuance is audited as it happens (oauth_handlers.go). Reads are
// audited in summary: a record per client, customer and route for each
// interval in which that client read that route, with the number of
// successful reads. One record per read would bury the audit trail under
// an integration's polling; the summary answers "who read what, and when"
// to within the interval.
const readAuditInterval = 5 * time.Minute

type readAuditKey struct{ client, customer, route string }

type readAudit struct {
	mu     sync.Mutex
	counts map[readAuditKey]int
	since  time.Time
}

// readAudits collects the adapter's successful reads between flushes.
var readAudits = &readAudit{counts: map[readAuditKey]int{}, since: time.Now().UTC()}

// note counts one successful read. route is the matched route pattern,
// never the path: a path carries account and device IDs, and the summary
// is about how much of what was read, per customer.
func (a *readAudit) note(client, customer, route string) {
	if route == "" {
		return
	}
	a.mu.Lock()
	a.counts[readAuditKey{client, customer, route}]++
	a.mu.Unlock()
}

func (a *readAudit) take() (map[readAuditKey]int, time.Time, time.Time) {
	now := time.Now().UTC()
	a.mu.Lock()
	defer a.mu.Unlock()
	counts, since := a.counts, a.since
	a.counts, a.since = map[readAuditKey]int{}, now
	return counts, since, now
}

// flush writes the interval's summary to the audit trail.
func (a *readAudit) flush(ctx context.Context, auditor *observability.Auditor, logf func(string, ...any)) {
	counts, from, to := a.take()
	for k, n := range counts {
		details := map[string]any{"route": k.route, "reads": n, "from": from, "to": to}
		if k.customer != "" {
			details["customer_id"] = k.customer
		}
		if err := auditor.Record(ctx, k.client, "", "BSSReadsSummary", details); err != nil {
			logf("failed to write read summary to the audit trail", "err", err, "client", k.client)
		}
	}
}

// runReadAuditLoop flushes on the interval and once more at shutdown, so
// the reads of the last few minutes before a restart are not lost.
func (h *handler) runReadAuditLoop(ctx context.Context) {
	ticker := time.NewTicker(readAuditInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			readAudits.flush(flushCtx, h.auditor, h.logger.Error)
			cancel()
			return
		case <-ticker.C:
			readAudits.flush(ctx, h.auditor, h.logger.Error)
		}
	}
}

// statusRecorder remembers the status a handler answered with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// auditedRead serves an authenticated request and, if it was a successful
// read of the integration surface, counts it for the client.
func auditedRead(next http.Handler, w http.ResponseWriter, r *http.Request, client, customer string) {
	if r.Method != http.MethodGet {
		next.ServeHTTP(w, r)
		return
	}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(rec, r)
	// r.Pattern is filled in by the mux while serving.
	if rec.status/100 == 2 {
		readAudits.note(client, customer, r.Pattern)
	}
}
