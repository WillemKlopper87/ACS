package ratelimit

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Two limiters on one database stand for two replicas: together they
// allow the limit once, not once each.
func TestSharedLimitHoldsAcrossReplicas(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS auth_attempt_windows (
		key_digest TEXT NOT NULL, window_start TIMESTAMPTZ NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (key_digest, window_start))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM auth_attempt_windows`); err != nil {
		t.Fatal(err)
	}
	a, b := NewShared(db, "login", 6, time.Hour), NewShared(db, "login", 6, time.Hour)
	allowed := 0
	for i := 0; i < 10; i++ {
		replica := a
		if i%2 == 1 {
			replica = b
		}
		ok, err := replica.Allow(ctx, "alice|203.0.113.7")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			allowed++
		}
	}
	if allowed != 6 {
		t.Fatalf("two replicas allowed %d attempts, want the shared limit of 6", allowed)
	}
	// Another key, and another limiter's scope, have their own count.
	if ok, _ := a.Allow(ctx, "bob|203.0.113.7"); !ok {
		t.Fatal("another key was refused")
	}
	if ok, _ := NewShared(db, "mfa", 6, time.Hour).Allow(ctx, "alice|203.0.113.7"); !ok {
		t.Fatal("another scope shares the count")
	}
	// The table holds no username or address.
	var leaked int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM auth_attempt_windows WHERE key_digest LIKE '%alice%' OR key_digest LIKE '%203.0%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("keys stored in the clear: %d (%v)", leaked, err)
	}
	// No limiter configured: everything is allowed.
	var none *Shared
	if ok, err := none.Allow(ctx, "x"); !ok || err != nil {
		t.Fatal("a nil limiter refused")
	}
	// A broken database does not lock anyone out.
	closed, _ := sql.Open("pgx", dsn)
	closed.Close()
	if ok, err := NewShared(closed, "login", 1, time.Hour).Allow(ctx, "x"); !ok || err == nil {
		t.Fatalf("with the database gone: allowed=%v err=%v, want allowed with an error", ok, err)
	}
}
