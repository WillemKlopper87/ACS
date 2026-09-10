package bss

import (
	"context"
	"errors"
	"os"
	"testing"

	"acs/internal/store"
)

// newMappingTestRepo mirrors newOAuthTestRepo: a clean, fully migrated
// schema per test.
func newMappingTestRepo(t *testing.T) (context.Context, *Repository) {
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

// seedDevice inserts the minimum a devices row needs so a mapping can
// reference it. oui_serial is the only NOT NULL column without a default.
func seedDevice(t *testing.T, ctx context.Context, r *Repository, id, ouiSerial string) {
	t.Helper()
	if _, err := r.db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ($1, $2)`, id, ouiSerial); err != nil {
		t.Fatalf("seed device %s: %v", ouiSerial, err)
	}
}

// rawAssign writes an assignment row directly, bypassing the repository's
// write path, so the read-side tests do not depend on Task 3.
func rawAssign(t *testing.T, ctx context.Context, r *Repository, id, accountID, deviceID, ouiSerial, role string, released bool) {
	t.Helper()
	q := `INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, status, role) VALUES ($1, $2, $3, $4, 'ACTIVE', $5)`
	if _, err := r.db.ExecContext(ctx, q, id, accountID, deviceID, ouiSerial, role); err != nil {
		t.Fatalf("raw assign: %v", err)
	}
	if released {
		if _, err := r.db.ExecContext(ctx, `UPDATE account_device_mappings SET unassigned_at = now(), unassign_reason = 'rma' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
}

const (
	devA = "aaaaaaaa-0000-0000-0000-000000000001"
	devB = "aaaaaaaa-0000-0000-0000-000000000002"
	devC = "aaaaaaaa-0000-0000-0000-000000000003"
	mapA = "bbbbbbbb-0000-0000-0000-000000000001"
	mapB = "bbbbbbbb-0000-0000-0000-000000000002"
	mapC = "bbbbbbbb-0000-0000-0000-000000000003"
)

func TestActiveDeviceForAccount_ResolvesByRole(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	seedDevice(t, ctx, r, devB, "S-B")
	rawAssign(t, ctx, r, mapA, "acct", devA, "S-A", RoleGateway, false)
	rawAssign(t, ctx, r, mapB, "acct", devB, "S-B", RoleONT, false)

	got, err := r.ActiveDeviceForAccount(ctx, "acct", RoleGateway)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != devA || got.Role != RoleGateway {
		t.Errorf("gateway resolved to %s/%s, want %s/gateway", got.DeviceID, got.Role, devA)
	}
	got, err = r.ActiveDeviceForAccount(ctx, "acct", RoleONT)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != devB {
		t.Errorf("ont resolved to %s, want %s", got.DeviceID, devB)
	}
}

func TestActiveDeviceForAccount_UnfilledRoleIsTypedError(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	rawAssign(t, ctx, r, mapA, "acct", devA, "S-A", RoleGateway, false)

	_, err := r.ActiveDeviceForAccount(ctx, "acct", RoleExtender)
	if !errors.Is(err, ErrNoDeviceForRole) {
		t.Errorf("unfilled role returned %v, want ErrNoDeviceForRole", err)
	}
}

// A released assignment must be invisible to every current-state read but
// present in history. This is the property ListAll and Stats would silently
// lose without the predicate.
func TestReleasedAssignmentsAreHistoryNotCurrent(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	seedDevice(t, ctx, r, devB, "S-B")
	rawAssign(t, ctx, r, mapA, "acct", devA, "S-A", RoleGateway, true)  // released
	rawAssign(t, ctx, r, mapB, "acct", devB, "S-B", RoleGateway, false) // current

	if _, err := r.ActiveDeviceForAccount(ctx, "acct", RoleGateway); err != nil {
		t.Fatalf("current gateway not resolvable: %v", err)
	}

	byAcct, err := r.ListByAccount(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(byAcct) != 1 || byAcct[0].DeviceID != devB {
		t.Errorf("ListByAccount = %+v, want only the current device %s", byAcct, devB)
	}

	all, err := r.ListAll(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("ListAll returned %d rows, want 1 (released row must be excluded)", len(all))
	}

	stats, err := r.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.MappingsByStatus["ACTIVE"] != 1 {
		t.Errorf("Stats counted %d ACTIVE mappings, want 1 (released row must be excluded)", stats.MappingsByStatus["ACTIVE"])
	}

	hist, err := r.AssignmentHistory(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("AssignmentHistory returned %d rows, want 2", len(hist))
	}
	if hist[0].UnassignedAt == nil || hist[0].UnassignReason != ReasonRMA {
		t.Errorf("first history row should be the released one with reason rma, got %+v", hist[0])
	}
	if hist[1].UnassignedAt != nil {
		t.Errorf("second history row should be current (UnassignedAt nil), got %+v", hist[1])
	}
}
