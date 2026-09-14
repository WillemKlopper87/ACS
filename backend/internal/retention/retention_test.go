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

	// Insert a capture_sessions row with status='ACTIVE' and expires_at = now() - 2 days.
	// This should NOT be pruned -- ACTIVE rows are never pruned regardless of age.
	activeID := uuid.New()
	activeExpiresAt := time.Now().UTC().Add(-2 * 24 * time.Hour)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO capture_sessions (id, match_type, match_value, protocol, status, started_by, started_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, activeID, "device", "device-key-2", "CWMP", "ACTIVE", "test-user", time.Now().UTC(), activeExpiresAt); err != nil {
		t.Fatalf("insert active session: %v", err)
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

	// Verify that the ACTIVE session was NOT deleted.
	var activeCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM capture_sessions WHERE id = $1`, activeID).Scan(&activeCount); err != nil {
		t.Fatalf("query active session count: %v", err)
	}
	if activeCount != 1 {
		t.Errorf("active session was deleted; want 1, got %d", activeCount)
	}

	// Verify the result reports the deletion.
	if result["capture_sessions"] != 1 {
		t.Errorf("result[\"capture_sessions\"] = %d, want 1", result["capture_sessions"])
	}
}
