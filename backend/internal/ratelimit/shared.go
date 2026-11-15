package ratelimit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

// Shared counts attempts in the database, so the limit holds across every
// replica of a service rather than per process (finding O10). It is a
// fixed window: at most Limit attempts per key in each Window.
//
// It is meant to sit behind the in-memory Limiter, which stays the first
// and cheapest check: Shared costs one small write per attempt, and only
// attempts the in-memory limiter let through reach it.
type Shared struct {
	db     *sql.DB
	scope  string
	limit  int
	window time.Duration
	calls  atomic.Uint64
}

// NewShared limits each key to limit attempts per window. scope keeps the
// keys of different limiters apart ("login", "mfa").
func NewShared(db *sql.DB, scope string, limit int, window time.Duration) *Shared {
	return &Shared{db: db, scope: scope, limit: limit, window: window}
}

// Allow counts one attempt for key and reports whether it is within the
// limit. A nil *Shared allows everything, so a caller without a database
// limiter needs no branch.
//
// If the count cannot be read or written the attempt is allowed and the
// error returned: the in-memory limiter still applies, and a database
// fault must not lock every operator out.
func (s *Shared) Allow(ctx context.Context, key string) (bool, error) {
	if s == nil {
		return true, nil
	}
	sum := sha256.Sum256([]byte(s.scope + "\x00" + key))
	digest := hex.EncodeToString(sum[:])
	var attempts int
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO auth_attempt_windows (key_digest, window_start, attempts)
		VALUES ($1, to_timestamp(floor(extract(epoch FROM now()) / $2) * $2), 1)
		ON CONFLICT (key_digest, window_start) DO UPDATE SET attempts = auth_attempt_windows.attempts + 1
		RETURNING attempts`, digest, s.window.Seconds()).Scan(&attempts)
	if err != nil {
		return true, fmt.Errorf("shared attempt limit: %w", err)
	}
	// Old windows are of no use; clear them now and then.
	if s.calls.Add(1)%256 == 1 {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM auth_attempt_windows WHERE window_start < now() - interval '1 day'`)
	}
	return attempts <= s.limit, nil
}
