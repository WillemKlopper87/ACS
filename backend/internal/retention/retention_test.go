package retention

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"acs/internal/store"
)

// newMigratedDB returns a clean, fully migrated database. Open already
// returns *sql.DB, which has everything the assertions need.
func newMigratedDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed retention test")
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
	return ctx, db
}

func TestRun_PrunesOldStoppedCaptureSessions(t *testing.T) {
	ctx, db := newMigratedDB(t)

	// Insert a capture_sessions row with status='STOPPED' and stopped_at = now() - 2 days.
	// This should be pruned by the retention rule.
	stoppedID := uuid.New()
	stoppedAt := time.Now().UTC().Add(-2 * 24 * time.Hour)
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO capture_sessions (id, match_type, match_value, protocol, status, started_by, started_at, stopped_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, stoppedID, "device", "device-key-1", "CWMP", "STOPPED", "test-user", time.Now().UTC(), stoppedAt, expiresAt); err != nil {
		t.Fatalf("insert stopped session: %v", err)
	}

	// Insert an ACTIVE capture_sessions row with expires_at = now() - 2 days (past retention window).
	// This SHOULD be pruned by the new OR branch of the retention rule.
	activeExpiredID := uuid.New()
	activeExpiredAt := time.Now().UTC().Add(-2 * 24 * time.Hour)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO capture_sessions (id, match_type, match_value, protocol, status, started_by, started_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, activeExpiredID, "device", "device-key-2", "CWMP", "ACTIVE", "test-user", time.Now().UTC(), activeExpiredAt); err != nil {
		t.Fatalf("insert active expired session: %v", err)
	}

	// Insert an ACTIVE capture_sessions row with expires_at still in the future.
	// This should NOT be pruned -- a genuinely live session must be protected.
	activeLiveID := uuid.New()
	activeLiveAt := time.Now().UTC().Add(24 * time.Hour)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO capture_sessions (id, match_type, match_value, protocol, status, started_by, started_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, activeLiveID, "device", "device-key-3", "CWMP", "ACTIVE", "test-user", time.Now().UTC(), activeLiveAt); err != nil {
		t.Fatalf("insert active live session: %v", err)
	}

	// Run retention with 1-day policy.
	result, err := Run(ctx, db, Policy{CaptureSessionsDays: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify that the STOPPED session was deleted.
	var stoppedCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM capture_sessions WHERE id = $1`, stoppedID).Scan(&stoppedCount); err != nil {
		t.Fatalf("query stopped session count: %v", err)
	}
	if stoppedCount != 0 {
		t.Errorf("stopped session still exists; want 0, got %d", stoppedCount)
	}

	// Verify that the ACTIVE expired session was deleted.
	var activeExpiredCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM capture_sessions WHERE id = $1`, activeExpiredID).Scan(&activeExpiredCount); err != nil {
		t.Fatalf("query active expired session count: %v", err)
	}
	if activeExpiredCount != 0 {
		t.Errorf("active expired session still exists; want 0, got %d", activeExpiredCount)
	}

	// Verify that the ACTIVE live session was NOT deleted.
	var activeLiveCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM capture_sessions WHERE id = $1`, activeLiveID).Scan(&activeLiveCount); err != nil {
		t.Fatalf("query active live session count: %v", err)
	}
	if activeLiveCount != 1 {
		t.Errorf("active live session was deleted; want 1, got %d", activeLiveCount)
	}

	// Verify the result reports the deletions (2 rows deleted: stopped + active expired).
	if result["capture_sessions"] != 2 {
		t.Errorf("result[\"capture_sessions\"] = %d, want 2", result["capture_sessions"])
	}
}
