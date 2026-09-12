package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// TestMigration0054AppliesCleanly pins the shape Task 2's contract
// requires: a notify_jobs_queued() trigger function and a
// jobs_notify_queued AFTER INSERT trigger on jobs, both present after
// Migrate.
func TestMigration0054AppliesCleanly(t *testing.T) {
	ctx, db := newMigratedDB(t)

	var proCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pg_proc WHERE proname = 'notify_jobs_queued'`).
		Scan(&proCount); err != nil {
		t.Fatal(err)
	}
	if proCount != 1 {
		t.Errorf("pg_proc rows named notify_jobs_queued = %d, want 1", proCount)
	}

	var trigCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pg_trigger WHERE tgname = 'jobs_notify_queued'`).
		Scan(&trigCount); err != nil {
		t.Fatal(err)
	}
	if trigCount != 1 {
		t.Errorf("pg_trigger rows named jobs_notify_queued = %d, want 1", trigCount)
	}
}

// rawListenConn acquires a dedicated *sql.Conn, issues LISTEN <channel> on
// it, and returns both the *sql.Conn (so the caller can release it) and
// the unwrapped native *pgx.Conn (so the caller can block on
// WaitForNotification) -- the same db.Conn -> Raw -> stdlib.Conn -> Conn()
// path listen.go uses, reproduced here so this test exercises the
// trigger in isolation from QueueListener.
func rawListenConn(ctx context.Context, t *testing.T, db *sql.DB, channel string) (*sql.Conn, *pgx.Conn) {
	t.Helper()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire dedicated conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "LISTEN "+channel); err != nil {
		conn.Close()
		t.Fatalf("LISTEN %s: %v", channel, err)
	}
	var native *pgx.Conn
	if err := conn.Raw(func(driverConn any) error {
		sc, ok := driverConn.(*stdlib.Conn)
		if !ok {
			t.Fatalf("driver conn is %T, want *stdlib.Conn", driverConn)
		}
		native = sc.Conn()
		return nil
	}); err != nil {
		conn.Close()
		t.Fatalf("unwrap raw conn: %v", err)
	}
	return conn, native
}

// TestMigration0054NotifiesOnInsert confirms a jobs INSERT with
// status='QUEUED' fires a NOTIFY on acs_jobs_queued carrying the device
// id as text -- the trigger's whole job. It listens on a dedicated raw
// pgx connection directly (bypassing internal/jobs.QueueListener
// entirely) so a failure here points at the trigger/migration, not at
// the Go listener.
func TestMigration0054NotifiesOnInsert(t *testing.T) {
	ctx, db := newMigratedDB(t)

	listenConn, native := rawListenConn(ctx, t, db, "acs_jobs_queued")
	defer listenConn.Close()

	const deviceID = "11111111-1111-1111-1111-111111111111"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO devices (id, oui_serial) VALUES ($1, '001349-NR7101-A')`, deviceID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO jobs (id, command_key, device_id, type, status, payload, created_by)
		VALUES ('22222222-2222-2222-2222-222222222222', 'setparam_test_0001', $1, 'SET_PARAMETER', 'QUEUED', '{}', 'test')
	`, deviceID); err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	notification, err := native.WaitForNotification(waitCtx)
	if err != nil {
		t.Fatalf("WaitForNotification: %v", err)
	}
	if notification.Channel != "acs_jobs_queued" {
		t.Errorf("notification.Channel = %q, want acs_jobs_queued", notification.Channel)
	}
	if notification.Payload != deviceID {
		t.Errorf("notification.Payload = %q, want %q", notification.Payload, deviceID)
	}
}
