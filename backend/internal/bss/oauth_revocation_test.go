package bss

import (
	"context"
	"os"
	"testing"

	"acs/internal/store"
)

func newOAuthTestRepo(t *testing.T) *OAuthRepository {
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
	return NewOAuthRepository(db)
}

// TestOAuthRepository_IsRevoked is the P2.3 acceptance gate: revoking a
// client must be independently observable (not just "future token
// issuance refused", which VerifyCredentials already covered) so an
// already-issued token's holder can be cut off before its own expiry.
func TestOAuthRepository_IsRevoked(t *testing.T) {
	r := newOAuthTestRepo(t)
	ctx := context.Background()

	client, _, err := r.CreateClient(ctx, "test-client")
	if err != nil {
		t.Fatal(err)
	}

	revoked, err := r.IsRevoked(ctx, client.ClientID)
	if err != nil || revoked {
		t.Fatalf("IsRevoked before revocation = (%v, %v), want (false, nil)", revoked, err)
	}

	if err := r.RevokeClient(ctx, client.ID); err != nil {
		t.Fatal(err)
	}

	revoked, err = r.IsRevoked(ctx, client.ClientID)
	if err != nil || !revoked {
		t.Fatalf("IsRevoked after revocation = (%v, %v), want (true, nil)", revoked, err)
	}

	// An unknown client_id fails closed.
	revoked, err = r.IsRevoked(ctx, "no-such-client-id")
	if err != nil || !revoked {
		t.Fatalf("IsRevoked for unknown client_id = (%v, %v), want (true, nil) -- fail closed", revoked, err)
	}
}
