package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

// newMigratedDB returns a clean, fully migrated database. Open already
// returns *sql.DB, which has everything the assertions need.
func newMigratedDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed migration test")
	}
	ctx := context.Background()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}

// TestMigration0052_AssignmentSchema pins the shape spec §5 requires: the
// pair-unique constraint is gone, the two partial unique indexes over
// active rows exist, and role backfills to gateway.
func TestMigration0052_AssignmentSchema(t *testing.T) {
	ctx, db := newMigratedDB(t)

	// The old pair-unique constraint must be gone.
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_constraint
		WHERE conname = 'account_device_mappings_account_id_device_id_key'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("old constraint account_device_mappings_account_id_device_id_key still exists")
	}

	// Both partial unique indexes must exist and be partial.
	for _, idx := range []string{"account_device_mappings_active_idx", "account_device_mappings_active_role_idx"} {
		var def string
		if err := db.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = $1`, idx).Scan(&def); err != nil {
			t.Fatalf("index %s: %v", idx, err)
		}
		if !strings.Contains(def, "UNIQUE") || !strings.Contains(def, "WHERE (unassigned_at IS NULL)") {
			t.Errorf("index %s is not a partial unique index over active rows: %s", idx, def)
		}
	}

	// A device and a mapping inserted without a role must backfill to gateway.
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ('11111111-1111-1111-1111-111111111111', '001349-NR7101-A')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, status)
		VALUES ('22222222-2222-2222-2222-222222222222', 'acct-1', '11111111-1111-1111-1111-111111111111', '001349-NR7101-A', 'ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	var role string
	var unassigned interface{}
	if err := db.QueryRowContext(ctx, `SELECT role, unassigned_at FROM account_device_mappings WHERE account_id = 'acct-1'`).Scan(&role, &unassigned); err != nil {
		t.Fatal(err)
	}
	if role != "gateway" {
		t.Errorf("role backfilled to %q, want gateway", role)
	}
	if unassigned != nil {
		t.Errorf("new row has unassigned_at set; want NULL (active)")
	}
}

// TestMigration0052_RoleUniqueRejectsSecondGateway is the constraint that
// makes addressing safe: one active device per role per account.
func TestMigration0052_RoleUniqueRejectsSecondGateway(t *testing.T) {
	ctx, db := newMigratedDB(t)
	for i, serial := range []string{"001349-NR7101-B", "001349-NR7101-C"} {
		id := "3333333" + string(rune('0'+i)) + "-3333-3333-3333-333333333333"
		if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ($1, $2)`, id, serial); err != nil {
			t.Fatal(err)
		}
	}
	first := `INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, status, role)
	          VALUES ('44444444-4444-4444-4444-444444444444', 'acct-2', '33333330-3333-3333-3333-333333333333', '001349-NR7101-B', 'ACTIVE', 'gateway')`
	if _, err := db.ExecContext(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := `INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, status, role)
	           VALUES ('55555555-5555-5555-5555-555555555555', 'acct-2', '33333331-3333-3333-3333-333333333333', '001349-NR7101-C', 'ACTIVE', 'gateway')`
	if _, err := db.ExecContext(ctx, second); err == nil {
		t.Fatal("second active gateway for the same account was accepted; the role-unique index is not enforcing")
	}
	// Releasing the first must allow the second.
	if _, err := db.ExecContext(ctx, `UPDATE account_device_mappings SET unassigned_at = now(), unassign_reason = 'rma' WHERE id = '44444444-4444-4444-4444-444444444444'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, second); err != nil {
		t.Fatalf("second gateway rejected after the first was released: %v", err)
	}
}
