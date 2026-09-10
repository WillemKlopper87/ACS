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

func TestAssignDevice_RejectsSecondDeviceInSameRole(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	seedDevice(t, ctx, r, devB, "S-B")

	if _, err := r.AssignDevice(ctx, "acct", "S-A", RoleGateway, "plan-1"); err != nil {
		t.Fatalf("first assign: %v", err)
	}
	_, err := r.AssignDevice(ctx, "acct", "S-B", RoleGateway, "plan-1")
	if !errors.Is(err, ErrRoleAlreadyAssigned) {
		t.Errorf("second gateway returned %v, want ErrRoleAlreadyAssigned", err)
	}
	// A different role is fine.
	if _, err := r.AssignDevice(ctx, "acct", "S-B", RoleONT, "plan-1"); err != nil {
		t.Errorf("assigning S-B as ont failed: %v", err)
	}
}

func TestAssignDevice_UnknownSerialIsDeviceNotFound(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	_, err := r.AssignDevice(ctx, "acct", "NO-SUCH", RoleGateway, "")
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("got %v, want ErrDeviceNotFound", err)
	}
}

func TestUnassignThenReassignSameDevice(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")

	if _, err := r.AssignDevice(ctx, "acct", "S-A", RoleGateway, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.UnassignDevice(ctx, "acct", RoleGateway, ReasonReturn); err != nil {
		t.Fatalf("unassign: %v", err)
	}
	if _, err := r.ActiveDeviceForAccount(ctx, "acct", RoleGateway); !errors.Is(err, ErrNoDeviceForRole) {
		t.Errorf("after unassign, gateway resolved: %v", err)
	}
	// The released device can come back — to the same account, even.
	if _, err := r.AssignDevice(ctx, "acct", "S-A", RoleGateway, ""); err != nil {
		t.Fatalf("reassign after release: %v", err)
	}
	hist, err := r.AssignmentHistory(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].UnassignReason != ReasonReturn || hist[1].UnassignedAt != nil {
		t.Errorf("history = %+v, want [released(return), current]", hist)
	}
}

func TestUnassignDevice_NothingToReleaseIsTypedError(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	err := r.UnassignDevice(ctx, "acct", RoleGateway, ReasonRMA)
	if !errors.Is(err, ErrNoDeviceForRole) {
		t.Errorf("got %v, want ErrNoDeviceForRole", err)
	}
}

// SwapDevice is the reason the temporal model exists. Close-then-open in
// one transaction: after it, exactly one gateway is active, it is the new
// device, and the old one is in history with the reason recorded.
func TestSwapDevice_ClosesOldOpensNewAtomically(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	seedDevice(t, ctx, r, devB, "S-B")
	if _, err := r.AssignDevice(ctx, "acct", "S-A", RoleGateway, "plan-1"); err != nil {
		t.Fatal(err)
	}

	got, err := r.SwapDevice(ctx, "acct", RoleGateway, "S-B", ReasonRMA)
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	if got.DeviceID != devB || got.UnassignedAt != nil {
		t.Errorf("swap returned %+v, want current assignment of %s", got, devB)
	}
	cur, err := r.ActiveDeviceForAccount(ctx, "acct", RoleGateway)
	if err != nil || cur.DeviceID != devB {
		t.Errorf("after swap, active gateway = %+v (err %v), want %s", cur, err, devB)
	}
	hist, err := r.AssignmentHistory(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].DeviceID != devA || hist[0].UnassignReason != ReasonRMA {
		t.Errorf("history = %+v, want old device released with reason rma first", hist)
	}
	// The service plan carries over to the replacement.
	if got.ServicePlan != "plan-1" {
		t.Errorf("service_plan after swap = %q, want plan-1 carried over", got.ServicePlan)
	}
}

// If the replacement cannot be inserted, the release must roll back —
// otherwise the account is left with NO gateway, worse than the defect
// being fixed. An unknown serial is the cheapest way to force that path.
func TestSwapDevice_RollsBackReleaseWhenInsertFails(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devA, "S-A")
	if _, err := r.AssignDevice(ctx, "acct", "S-A", RoleGateway, ""); err != nil {
		t.Fatal(err)
	}

	_, err := r.SwapDevice(ctx, "acct", RoleGateway, "NO-SUCH", ReasonRMA)
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("swap to unknown serial returned %v, want ErrDeviceNotFound", err)
	}
	cur, err := r.ActiveDeviceForAccount(ctx, "acct", RoleGateway)
	if err != nil || cur.DeviceID != devA {
		t.Errorf("after failed swap, active gateway = %+v (err %v); the release was not rolled back", cur, err)
	}
}

func TestSwapDevice_NothingToSwapIsTypedError(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, devB, "S-B")
	_, err := r.SwapDevice(ctx, "acct", RoleGateway, "S-B", ReasonRMA)
	if !errors.Is(err, ErrNoDeviceForRole) {
		t.Errorf("got %v, want ErrNoDeviceForRole", err)
	}
}
