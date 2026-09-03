package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"acs/internal/store"
)

func newTestReplayStore(t *testing.T) *PostgresReplayStore {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return NewPostgresReplayStore(db)
}

// TestPostgresReplayStore_CheckAndRecord is the P1.6 acceptance gate at
// the storage layer: a (nonce, nc) pair records once; a repeat of the
// same nc is a replay; a strictly greater nc on the same nonce (the
// qop=auth case) is accepted; going backwards is a replay too.
func TestPostgresReplayStore_CheckAndRecord(t *testing.T) {
	s := newTestReplayStore(t)
	ctx := context.Background()
	expires := time.Now().Add(10 * time.Minute)

	ok, err := s.CheckAndRecord(ctx, "nonceA", 1, expires)
	if err != nil || !ok {
		t.Fatalf("first use of (nonceA, 1) = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := s.CheckAndRecord(ctx, "nonceA", 1, expires); err != nil || ok {
		t.Errorf("repeat of (nonceA, 1) = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := s.CheckAndRecord(ctx, "nonceA", 3, expires); err != nil || !ok {
		t.Errorf("(nonceA, 3) after 1 = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := s.CheckAndRecord(ctx, "nonceA", 2, expires); err != nil || ok {
		t.Errorf("(nonceA, 2) after 3 = (%v, %v), want (false, nil) — nc must strictly increase", ok, err)
	}
	// A different nonce is entirely independent.
	if ok, err := s.CheckAndRecord(ctx, "nonceB", 1, expires); err != nil || !ok {
		t.Errorf("first use of (nonceB, 1) = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestPostgresReplayStore_Purge(t *testing.T) {
	s := newTestReplayStore(t)
	ctx := context.Background()
	if _, err := s.CheckAndRecord(ctx, "expired", 1, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckAndRecord(ctx, "live", 1, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := s.Purge(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Purge removed %d rows, want 1 (only the expired one)", n)
	}
	// The live nonce's replay protection survived the purge.
	if ok, err := s.CheckAndRecord(ctx, "live", 1, time.Now().Add(time.Hour)); err != nil || ok {
		t.Errorf("replay of the still-live nonce after Purge = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestDigestAuthenticator_ReplayRejectedAcrossReplicas is the P1.6
// acceptance gate at the Verify() layer: two independently constructed
// DigestAuthenticator values — standing in for two cmd/acs replicas
// behind a load balancer — sharing only a PostgresReplayStore (never
// each other's process memory) must still reject a replayed
// Authorization header, which the in-process-cache default could not do.
func TestDigestAuthenticator_ReplayRejectedAcrossReplicas(t *testing.T) {
	replayStore := newTestReplayStore(t)

	newReplica := func() DigestAuthenticator {
		return DigestAuthenticator{Username: "cpe-device", Password: "s3cret", ReplayStore: replayStore}
	}
	replicaA := newReplica()
	replicaB := newReplica()

	nonce := replicaA.newNonce(time.Now())
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/cwmp", nil)
		r.Header.Set("Authorization", buildAuthHeader("cpe-device", "s3cret", http.MethodPost, "/cwmp", nonce, "00000001", "c"))
		return r
	}

	ok, _, _ := replicaA.Verify(req())
	if !ok {
		t.Fatal("replica A rejected a valid, first-use Digest response")
	}
	// The identical Authorization header (same nonce+nc), replayed
	// against a *different* authenticator instance that never saw
	// replica A's in-memory state — only Postgres connects them.
	ok, _, _ = replicaB.Verify(req())
	if ok {
		t.Error("replica B accepted a header already consumed by replica A — cross-replica replay protection failed")
	}
}
