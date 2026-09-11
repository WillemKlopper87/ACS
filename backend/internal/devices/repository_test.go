package devices

import (
	"context"
	"os"
	"testing"

	"acs/internal/cwmp"
	"acs/internal/store"
)

// newDevicesTestRepo mirrors the DSN-skip harness used throughout the
// backend's other DB-backed test suites (e.g. internal/bss/mapping_test.go's
// newMappingTestRepo): a clean, fully migrated schema per test, skipped
// entirely when no live Postgres is configured for tests.
func newDevicesTestRepo(t *testing.T) (context.Context, *Repository) {
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
	return ctx, NewRepository(db)
}

// TestUpsertFromInformSetsCWMPManagementProtocol covers the extension made
// to UpsertFromInform alongside Task 3's UpsertFromOnBoard: a device that
// has only ever Informed over CWMP must have 'CWMP' recorded in
// management_protocols (added by migration 0053), both on first insert and
// idempotently across repeated Informs from the same identity.
func TestUpsertFromInformSetsCWMPManagementProtocol(t *testing.T) {
	ctx, r := newDevicesTestRepo(t)
	id := cwmp.DeviceID{OUI: "001122", ProductClass: "Router", SerialNumber: "ABC123"}

	d1, err := r.UpsertFromInform(ctx, id, []string{"0 BOOTSTRAP"})
	if err != nil {
		t.Fatalf("first UpsertFromInform: %v", err)
	}
	protocols := managementProtocols(t, ctx, r, d1.ID)
	if !containsAll(protocols, "CWMP") {
		t.Errorf("management_protocols after first Inform = %v, want to contain CWMP", protocols)
	}

	d2, err := r.UpsertFromInform(ctx, id, []string{"2 PERIODIC"})
	if err != nil {
		t.Fatalf("second UpsertFromInform: %v", err)
	}
	if d2.ID != d1.ID {
		t.Fatalf("second Inform created a new row: %s != %s", d2.ID, d1.ID)
	}
	protocols = managementProtocols(t, ctx, r, d1.ID)
	if len(protocols) != 1 || protocols[0] != "CWMP" {
		t.Errorf("management_protocols after repeated CWMP Inform = %v, want exactly [CWMP] (no duplicates)", protocols)
	}
}

// managementProtocols reads the column directly — it is not part of the
// Device struct (a scan-shape decision out of scope for this task), so
// tests that need to assert on it query it themselves.
func managementProtocols(t *testing.T, ctx context.Context, r *Repository, deviceID string) []string {
	t.Helper()
	var protocols store.StringArray
	if err := r.db.QueryRowContext(ctx, `SELECT management_protocols FROM devices WHERE id = $1`, deviceID).Scan(&protocols); err != nil {
		t.Fatalf("read management_protocols: %v", err)
	}
	return []string(protocols)
}

func containsAll(haystack []string, needles ...string) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if h == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
