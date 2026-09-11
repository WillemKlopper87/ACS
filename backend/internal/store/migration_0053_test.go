package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestMigration0053AppliesCleanly pins the shape §Task 1's contract
// requires: devices gains management_protocols as a NOT NULL array
// defaulting to '{}', so existing rows backfill to an empty array rather
// than NULL.
func TestMigration0053AppliesCleanly(t *testing.T) {
	ctx, db := newMigratedDB(t)

	var dataType, isNullable string
	if err := db.QueryRowContext(ctx, `
		SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_name = 'devices' AND column_name = 'management_protocols'`).
		Scan(&dataType, &isNullable); err != nil {
		t.Fatal(err)
	}
	if dataType != "ARRAY" {
		t.Errorf("devices.management_protocols data_type = %q, want ARRAY", dataType)
	}
	if isNullable != "NO" {
		t.Errorf("devices.management_protocols is_nullable = %q, want NO", isNullable)
	}

	// A device inserted without specifying the column must default to '{}',
	// not NULL.
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ('11111111-1111-1111-1111-111111111111', '001349-NR7101-A')`); err != nil {
		t.Fatal(err)
	}
	var protocols StringArray
	if err := db.QueryRowContext(ctx, `SELECT management_protocols FROM devices WHERE id = '11111111-1111-1111-1111-111111111111'`).Scan(&protocols); err != nil {
		t.Fatal(err)
	}
	if len(protocols) != 0 {
		t.Errorf("management_protocols = %v, want empty", protocols)
	}
}

// TestMigration0053EndpointIDUnique confirms the database durably enforces
// what mtp.Registry already enforces in-process: at most one live agent
// per endpoint id.
func TestMigration0053EndpointIDUnique(t *testing.T) {
	ctx, db := newMigratedDB(t)

	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES
		('11111111-1111-1111-1111-111111111111', '001349-NR7101-A'),
		('22222222-2222-2222-2222-222222222222', '001349-NR7101-B')`); err != nil {
		t.Fatal(err)
	}

	first := `INSERT INTO usp_agents (device_id, endpoint_id, mtp_kind)
	          VALUES ('11111111-1111-1111-1111-111111111111', 'os::endpoint-1', 'WebSocket')`
	if _, err := db.ExecContext(ctx, first); err != nil {
		t.Fatal(err)
	}

	second := `INSERT INTO usp_agents (device_id, endpoint_id, mtp_kind)
	           VALUES ('22222222-2222-2222-2222-222222222222', 'os::endpoint-1', 'MQTT')`
	_, err := db.ExecContext(ctx, second)
	if err == nil {
		t.Fatal("second usp_agents row with the same endpoint_id was accepted; the unique constraint is not enforcing")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("got %v, want a pgconn.PgError with code 23505 (unique_violation)", err)
	}
}

// TestMigration0053MTPKindCheck confirms mtp_kind is constrained to the
// exact strings mtp.KindWebSocket and mtp.KindMQTT carry — no other MTP
// value can reach the row.
func TestMigration0053MTPKindCheck(t *testing.T) {
	ctx, db := newMigratedDB(t)

	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ('11111111-1111-1111-1111-111111111111', '001349-NR7101-A')`); err != nil {
		t.Fatal(err)
	}

	bad := `INSERT INTO usp_agents (device_id, endpoint_id, mtp_kind)
	        VALUES ('11111111-1111-1111-1111-111111111111', 'os::endpoint-1', 'STOMP')`
	_, err := db.ExecContext(ctx, bad)
	if err == nil {
		t.Fatal("usp_agents row with mtp_kind = 'STOMP' was accepted; the CHECK constraint is not enforcing")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Errorf("got %v, want a pgconn.PgError with code 23514 (check_violation)", err)
	}
}
