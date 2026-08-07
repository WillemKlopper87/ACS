// Package ratelimit provides a per-key in-memory token-bucket limiter
// (build plan §7.3): golang.org/x/time/rate for the bucket itself, the
// same in-memory map-plus-mutex shape internal/cwmp.SessionStore already
// uses for probe sessions. Explicitly per-process — fine for the
// single-instance deployment this build targets; a shared store (Redis)
// becomes necessary once Phase 7 makes multiple replicas real, per §7.3's
// own caveat. Not shared across cmd/acs/cmd/api/cmd/bssadapter processes,
// only reused as a package across them.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter hands out one token bucket per key — a bearer token, a device
// OUI+Serial, a client IP, whatever identity a caller has (build plan
// §7.1's per-surface table). Buckets idle past idleTTL are evicted lazily
// on the next Allow call, so a churn of one-off keys (e.g. IPs from an
// unauthenticated flood) doesn't grow this map forever.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    rate.Limit
	burst   int
	idleTTL time.Duration
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// New creates a Limiter allowing ratePerSecond sustained requests per key
// with burst headroom above that.
func New(ratePerSecond float64, burst int, idleTTL time.Duration) *Limiter {
	return &Limiter{
		buckets: make(map[string]*bucket),
		rate:    rate.Limit(ratePerSecond),
		burst:   burst,
		idleTTL: idleTTL,
	}
}

// Allow reports whether a request for key may proceed right now,
// consuming one token from its bucket if so.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.evictIdleLocked()

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{limiter: rate.NewLimiter(l.rate, l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = time.Now()
	return b.limiter.Allow()
}

// evictIdleLocked drops buckets idle past idleTTL. Full-scan on every
// Allow call — fine for the small, low-cardinality key sets this is used
// with today (a handful of bearer tokens or fleet devices, not millions
// of keys); would need a smarter sweep before this scales past that.
// Called under l.mu.
func (l *Limiter) evictIdleLocked() {
	if l.idleTTL <= 0 {
		return
	}
	cutoff := time.Now().Add(-l.idleTTL)
	for k, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}
