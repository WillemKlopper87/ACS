package ratelimit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// SharedEnv turns the cross-replica request limit on. Set it when a
// service runs as more than one replica: with one, the in-memory Limiter
// already is the whole limit and this would only add a query a second.
const SharedEnv = "ACS_SHARED_RATE_LIMIT"

// Synced makes a request-rate limit hold across the replicas of a
// service (finding O10). The in-memory Limiter is per process, so N
// replicas allowed a caller N times the rate.
//
// A database write per request would cost more than the requests it
// protects, so replicas do not coordinate on each one. Each counts
// locally, and about once a second (Run) adds its counts to a shared row
// per caller and time window, reading back the total. A caller whose
// total is over the limit is refused by every replica for the rest of
// that window.
//
// It is therefore approximate: a caller can exceed the limit by what the
// replicas let through in the second before they hear of each other. It
// bounds a caller's total rate; the in-memory Limiter, which still runs
// first, bounds its bursts.
type Synced struct {
	db     *sql.DB
	scope  string
	limit  int
	window time.Duration
	every  time.Duration

	mu      sync.Mutex
	pending map[string]int
	blocked map[string]time.Time
	ticks   int
}

// NewSynced allows each caller, across all replicas, what one process's
// Limiter would allow over a window: perSecond for the length of the
// window, plus the burst.
func NewSynced(db *sql.DB, scope string, perSecond float64, burst int, window time.Duration) *Synced {
	return &Synced{
		db: db, scope: scope, window: window, every: time.Second,
		limit:   int(perSecond*window.Seconds()) + burst,
		pending: map[string]int{}, blocked: map[string]time.Time{},
	}
}

// Allow counts one request and reports whether the caller is within the
// shared limit as far as this replica knows. A nil *Synced allows
// everything.
func (s *Synced) Allow(key string) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if until, ok := s.blocked[key]; ok {
		if time.Now().Before(until) {
			return false
		}
		delete(s.blocked, key)
	}
	s.pending[key]++
	return true
}

// BlockUntil refuses a caller until the given time, as an exchange that
// found it over the limit does.
func (s *Synced) BlockUntil(key string, until time.Time) {
	s.mu.Lock()
	s.blocked[key] = until
	s.mu.Unlock()
}

// Run exchanges counts with the database until ctx ends. onError is told
// of a failed exchange; the counts of that second are dropped, so a
// database fault loosens the shared limit rather than refusing callers.
func (s *Synced) Run(ctx context.Context, onError func(error)) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(s.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Sync(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}

// Sync does one exchange. Exported for tests.
func (s *Synced) Sync(ctx context.Context) error {
	s.mu.Lock()
	counts := s.pending
	s.pending = map[string]int{}
	s.ticks++
	cleanup := s.ticks%60 == 1
	s.mu.Unlock()
	if len(counts) == 0 && !cleanup {
		return nil
	}

	type add struct {
		K string `json:"k"`
		H int    `json:"h"`
	}
	adds := make([]add, 0, len(counts))
	keyOf := make(map[string]string, len(counts))
	for key, n := range counts {
		sum := sha256.Sum256([]byte(key))
		digest := hex.EncodeToString(sum[:])
		keyOf[digest] = key
		adds = append(adds, add{digest, n})
	}
	payload, _ := json.Marshal(adds)
	rows, err := s.db.QueryContext(ctx, `
		INSERT INTO rate_limit_windows (scope, key_digest, window_start, hits)
		SELECT $1, a.k, to_timestamp(floor(extract(epoch FROM now()) / $2) * $2), a.h
		  FROM jsonb_to_recordset($3::jsonb) AS a(k text, h int)
		ON CONFLICT (scope, key_digest, window_start) DO UPDATE SET hits = rate_limit_windows.hits + EXCLUDED.hits
		RETURNING key_digest, hits, window_start`, s.scope, s.window.Seconds(), string(payload))
	if err != nil {
		return fmt.Errorf("shared rate limit: %w", err)
	}
	defer rows.Close()
	over := map[string]time.Time{}
	for rows.Next() {
		var digest string
		var hits int
		var start time.Time
		if err := rows.Scan(&digest, &hits, &start); err != nil {
			return fmt.Errorf("shared rate limit: %w", err)
		}
		if hits > s.limit {
			over[keyOf[digest]] = start.Add(s.window)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("shared rate limit: %w", err)
	}
	now := time.Now()
	s.mu.Lock()
	for key, until := range over {
		s.blocked[key] = until
	}
	for key, until := range s.blocked {
		if !now.Before(until) {
			delete(s.blocked, key)
		}
	}
	s.mu.Unlock()
	if cleanup {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM rate_limit_windows WHERE scope = $1 AND window_start < now() - make_interval(secs => $2)`,
			s.scope, 10*s.window.Seconds()); err != nil {
			return fmt.Errorf("shared rate limit cleanup: %w", err)
		}
	}
	return nil
}
