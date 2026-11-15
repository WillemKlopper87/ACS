package ratelimit

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// Two limiters on one database stand for two replicas. A caller spread
// over both is stopped by both once their combined count passes the
// limit; a caller under it is not.
func TestSyncedLimitHoldsAcrossReplicas(t *testing.T) {
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed limiter test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS rate_limit_windows (
		scope TEXT NOT NULL, key_digest TEXT NOT NULL, window_start TIMESTAMPTZ NOT NULL, hits INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (scope, key_digest, window_start))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM rate_limit_windows`); err != nil {
		t.Fatal(err)
	}
	// 1 a second for an hour-long window plus a burst of 10: a limit far
	// above the test's "quiet" caller and far below its "busy" one, in a
	// window that cannot roll over mid-test.
	a := NewSynced(db, "api", 0.01, 10, time.Hour)
	b := NewSynced(db, "api", 0.01, 10, time.Hour)
	if a.limit != 46 {
		t.Fatalf("limit = %d, want 0.01/s over an hour plus a burst of 10", a.limit)
	}

	// Each replica sees 30 requests from "busy": under the limit alone,
	// over it together.
	for i := 0; i < 30; i++ {
		if !a.Allow("busy") || !b.Allow("busy") {
			t.Fatal("refused before the replicas had exchanged counts")
		}
		a.Allow("quiet")
	}
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if !a.Allow("busy") {
		t.Fatal("replica A refused on its own 30 requests alone")
	}
	if err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if b.Allow("busy") {
		t.Fatal("replica B still allows a caller that is over the limit across both")
	}
	// A hears of it at its next exchange.
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Allow("busy") {
		t.Fatal("replica A still allows the caller after exchanging counts")
	}
	if !a.Allow("quiet") || !b.Allow("quiet") {
		t.Fatal("a caller under the limit was refused")
	}
	// Another service's limiter has its own counts.
	other := NewSynced(db, "bss", 0.01, 10, time.Hour)
	other.Allow("busy")
	if err := other.Sync(ctx); err != nil || !other.Allow("busy") {
		t.Fatalf("another scope shares the count (%v)", err)
	}
	// Callers are stored as digests.
	var leaked int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM rate_limit_windows WHERE key_digest IN ('busy', 'quiet')`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("callers stored in the clear: %d (%v)", leaked, err)
	}
	// Off, and with the database gone, nobody is refused.
	var none *Synced
	if !none.Allow("x") {
		t.Fatal("a nil limiter refused")
	}
	closed, _ := sql.Open("pgx", dsn)
	closed.Close()
	broken := NewSynced(closed, "api", 0.01, 10, time.Hour)
	broken.Allow("x")
	if err := broken.Sync(ctx); err == nil || !broken.Allow("x") {
		t.Fatalf("with the database gone: err=%v, want an error and the caller still allowed", err)
	}
}
