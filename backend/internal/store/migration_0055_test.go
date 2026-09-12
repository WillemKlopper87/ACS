package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestMigration0055AppliesCleanly pins the shape the Contract requires:
// usp_subscriptions and device_events both exist after Migrate, with the
// device_events dedup unique constraint in place.
func TestMigration0055AppliesCleanly(t *testing.T) {
	ctx, db := newMigratedDB(t)

	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'usp_subscriptions'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("usp_subscriptions table count = %d, want 1", n)
	}

	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'device_events'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("device_events table count = %d, want 1", n)
	}

	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pg_constraint WHERE conname = 'device_events_device_id_msg_id_key'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("device_events UNIQUE(device_id, msg_id) constraint count = %d, want 1", n)
	}
}

// TestMigration0055NotifTypeCheck confirms usp_subscriptions.notif_type is
// constrained to the five USP notification types -- no other value can
// reach the row.
func TestMigration0055NotifTypeCheck(t *testing.T) {
	ctx, db := newMigratedDB(t)

	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ('11111111-1111-1111-1111-111111111111', '001349-NR7101-A')`); err != nil {
		t.Fatal(err)
	}

	bad := `INSERT INTO usp_subscriptions (id, device_id, notif_type, reference_list, created_by)
	        VALUES ('22222222-2222-2222-2222-222222222222', '11111111-1111-1111-1111-111111111111', 'Bogus', '{}', 'test')`
	_, err := db.ExecContext(ctx, bad)
	if err == nil {
		t.Fatal("usp_subscriptions row with notif_type = 'Bogus' was accepted; the CHECK constraint is not enforcing")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Errorf("got %v, want a pgconn.PgError with code 23514 (check_violation)", err)
	}
}

// TestMigration0055DeviceEventsDedup confirms device_events'
// UNIQUE(device_id, msg_id) makes an ON CONFLICT DO NOTHING insert of a
// duplicate (device_id, msg_id) a true no-op -- the row keeps the first
// insert's values, proving the conflict wasn't silently turned into an
// overwrite.
func TestMigration0055DeviceEventsDedup(t *testing.T) {
	ctx, db := newMigratedDB(t)

	const deviceID = "11111111-1111-1111-1111-111111111111"
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ($1, '001349-NR7101-A')`, deviceID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO device_events (device_id, msg_id, obj_path, event_name, params)
		VALUES ($1, 'msg-1', 'Device.WiFi.', 'ObjectCreation', '{}')`, deviceID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO device_events (device_id, msg_id, obj_path, event_name, params)
		VALUES ($1, 'msg-1', 'Device.Other.', 'ObjectDeletion', '{"x":1}')
		ON CONFLICT (device_id, msg_id) DO NOTHING`, deviceID); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM device_events WHERE device_id = $1`, deviceID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("device_events row count = %d, want 1", n)
	}

	var objPath, eventName string
	if err := db.QueryRowContext(ctx,
		`SELECT obj_path, event_name FROM device_events WHERE device_id = $1 AND msg_id = 'msg-1'`, deviceID).
		Scan(&objPath, &eventName); err != nil {
		t.Fatal(err)
	}
	if objPath != "Device.WiFi." {
		t.Errorf("obj_path = %q, want the first insert's %q (conflict was not a no-op)", objPath, "Device.WiFi.")
	}
	if eventName != "ObjectCreation" {
		t.Errorf("event_name = %q, want the first insert's %q (conflict was not a no-op)", eventName, "ObjectCreation")
	}
}
