package telemetry_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"acs/internal/bss"
	"acs/internal/store"
	"acs/internal/tmf/telemetry"
)

// TestRecoveryFollowsAFault: a SET_PARAMETER fault raises DeviceFault; a
// later GET_PARAMETER success leaves it; a later SET_PARAMETER success
// clears it with one DeviceRecovered event, and a second success adds none.
func TestRecoveryFollowsAFault(t *testing.T) {
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo := bss.NewRepository(db)
	const device = "bbbbbbbb-0000-0000-0000-000000000001"
	for _, q := range []string{
		`INSERT INTO devices (id, oui_serial) VALUES ($1, 'A4FC77+X+REC01')`,
		`INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, status, role) VALUES (gen_random_uuid(), 'acct-rec', $1, 'A4FC77+X+REC01', 'ACTIVE', 'gateway')`,
		`INSERT INTO jobs (id, command_key, device_id, type, status, payload) VALUES ('cccccccc-0000-0000-0000-000000000001', 'ck-fault', $1, 'SET_PARAMETER', 'FAILED', '{}')`,
	} {
		if _, err := db.ExecContext(ctx, q, device); err != nil {
			t.Fatal(err)
		}
	}
	if err := telemetry.PublishFault(ctx, repo, telemetry.Fault{AccountID: "acct-rec", DeviceID: device, JobID: "cccccccc-0000-0000-0000-000000000001",
		Protocol: "CWMP", Code: "9007", Message: "Invalid parameter value"}); err != nil {
		t.Fatal(err)
	}
	count := func(eventType string) int {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tmf_events WHERE event_type = $1`, eventType).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if err := telemetry.PublishRecoveries(ctx, repo, device, "CWMP", "GET_PARAMETER"); err != nil || count("DeviceRecovered") != 0 {
		t.Fatalf("a read recovered a write fault: %d, %v", count("DeviceRecovered"), err)
	}
	if err := telemetry.PublishRecoveries(ctx, repo, device, "CWMP", "SET_PARAMETER"); err != nil || count("DeviceRecovered") != 1 {
		t.Fatalf("recoveries after a successful write = %d, %v", count("DeviceRecovered"), err)
	}
	if err := telemetry.PublishRecoveries(ctx, repo, device, "CWMP", "SET_PARAMETER"); err != nil || count("DeviceRecovered") != 1 {
		t.Fatalf("a second success recovered again: %d, %v", count("DeviceRecovered"), err)
	}
	var state, payload, account string
	if err := db.QueryRowContext(ctx, `SELECT a.state, e.payload::text, COALESCE(e.account_id, '') FROM tmf_alarms a, tmf_events e
		WHERE a.alarm_type = 'DeviceFault' AND e.event_type = 'DeviceRecovered'`).Scan(&state, &payload, &account); err != nil {
		t.Fatal(err)
	}
	if state != "cleared" || account != "acct-rec" || !strings.Contains(payload, `"faultCode": "9007"`) {
		t.Fatalf("alarm %s, event %s for %q", state, payload, account)
	}
}
