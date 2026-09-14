package captures

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"acs/internal/store"
)

func newTestDB(t *testing.T) (context.Context, *sql.DB) {
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
	return ctx, db
}

func TestStartAndGet(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	s, err := r.Start(ctx, StartParams{
		MatchType: MatchDevice, MatchValue: "001349+S1", Protocol: "CWMP",
		StartedBy: "operator@example.com", MaxDuration: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.Status != StatusActive {
		t.Errorf("Status = %q, want ACTIVE", s.Status)
	}

	got, err := r.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MatchValue != "001349+S1" || got.Protocol != "CWMP" {
		t.Errorf("Get() = %+v, want the started session", got)
	}
}

func TestStartRejectsDuplicateActiveMatch(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	params := StartParams{MatchType: MatchIdentity, MatchValue: "001349+S2", Protocol: "CWMP", StartedBy: "op", MaxDuration: time.Minute}
	if _, err := r.Start(ctx, params); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if _, err := r.Start(ctx, params); err != ErrAlreadyActive {
		t.Errorf("second Start on the same (match_type, match_value) = %v, want ErrAlreadyActive", err)
	}
}

func TestStartAllowsSameMatchValueAfterStop(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	params := StartParams{MatchType: MatchRemoteIP, MatchValue: "10.0.0.5", Protocol: "USP", StartedBy: "op", MaxDuration: time.Minute}
	s1, err := r.Start(ctx, params)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := r.Stop(ctx, s1.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := r.Start(ctx, params); err != nil {
		t.Errorf("Start after Stop = %v, want success (the unique index is scoped to status=ACTIVE)", err)
	}
}

func TestStartAllowsSameMatchValueAfterExpiry(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	params := StartParams{MatchType: MatchIdentity, MatchValue: "001349+EXPIRED", Protocol: "CWMP", StartedBy: "op", MaxDuration: -time.Minute}
	first, err := r.Start(ctx, params)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	params.MaxDuration = time.Minute
	if _, err := r.Start(ctx, params); err != nil {
		t.Fatalf("Start after expiry = %v, want success", err)
	}

	got, err := r.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get expired session: %v", err)
	}
	if got.Status != StatusExpired {
		t.Errorf("expired session status = %q, want EXPIRED", got.Status)
	}
}

func TestActiveMatchFindsRunningSessionOnly(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	s, err := r.Start(ctx, StartParams{MatchType: MatchIdentity, MatchValue: "001349+S3", Protocol: "CWMP", StartedBy: "op", MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	found, err := r.ActiveMatch(ctx, "CWMP", "001349+S3", "")
	if err != nil {
		t.Fatalf("ActiveMatch: %v", err)
	}
	if len(found) != 1 || found[0].ID != s.ID {
		t.Fatalf("ActiveMatch = %+v, want exactly the started session", found)
	}

	if err := r.Stop(ctx, s.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	found, err = r.ActiveMatch(ctx, "CWMP", "001349+S3", "")
	if err != nil {
		t.Fatalf("ActiveMatch after stop: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("ActiveMatch after Stop = %+v, want none", found)
	}
}

func TestActiveMatchExcludesExpired(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	s, err := r.Start(ctx, StartParams{MatchType: MatchIdentity, MatchValue: "001349+S4", Protocol: "CWMP", StartedBy: "op", MaxDuration: -time.Minute})
	if err != nil {
		t.Fatalf("Start with already-past expiry: %v", err)
	}
	found, err := r.ActiveMatch(ctx, "CWMP", "001349+S4", "")
	if err != nil {
		t.Fatalf("ActiveMatch: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("ActiveMatch for an expired session = %+v, want none", found)
	}
	_ = s
}

func TestActiveMatchByRemoteIP(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	if _, err := r.Start(ctx, StartParams{MatchType: MatchRemoteIP, MatchValue: "192.168.1.50", Protocol: "USP", StartedBy: "op", MaxDuration: time.Minute}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	found, err := r.ActiveMatch(ctx, "USP", "", "192.168.1.50")
	if err != nil {
		t.Fatalf("ActiveMatch: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("ActiveMatch by remote IP = %+v, want exactly one", found)
	}
	found, err = r.ActiveMatch(ctx, "USP", "", "10.10.10.10")
	if err != nil {
		t.Fatalf("ActiveMatch: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("ActiveMatch for a non-matching IP = %+v, want none", found)
	}
}

func TestRecordEventAndListEventsOrdered(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	s, err := r.Start(ctx, StartParams{MatchType: MatchDevice, MatchValue: "001349+S5", Protocol: "CWMP", StartedBy: "op", MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	body1 := "<Inform>...</Inform>"
	if err := r.RecordEvent(ctx, s.ID, "inbound", "Inform", "Inform received", &body1); err != nil {
		t.Fatalf("RecordEvent 1: %v", err)
	}
	if err := r.RecordEvent(ctx, s.ID, "outbound", "SetParameterValues", "dispatched SetParameterValues", nil); err != nil {
		t.Fatalf("RecordEvent 2: %v", err)
	}

	events, err := r.ListEvents(ctx, s.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ListEvents = %d events, want 2", len(events))
	}
	if events[0].Seq != 1 || events[0].Kind != "Inform" || events[1].Seq != 2 || events[1].Kind != "SetParameterValues" {
		t.Errorf("events out of order or wrong: %+v", events)
	}
	if events[0].Body == nil || *events[0].Body != body1 {
		t.Errorf("events[0].Body = %v, want %q", events[0].Body, body1)
	}
	if events[1].Body != nil {
		t.Errorf("events[1].Body = %v, want nil", events[1].Body)
	}
}

func TestRecordEventForDeviceCorrelatesAtomically(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	var deviceID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number)
		VALUES (gen_random_uuid(), '001349+CORRELATED', 'Vendor', '001349', 'CPE', 'CORRELATED')
		RETURNING id`).Scan(&deviceID); err != nil {
		t.Fatalf("insert device: %v", err)
	}
	s, err := r.Start(ctx, StartParams{MatchType: MatchIdentity, MatchValue: "001349+CORRELATED", Protocol: "CWMP", StartedBy: "op", MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	body := "redacted transcript"
	if err := r.RecordEventForDevice(ctx, s.ID, deviceID, "inbound", "Inform", "correlated", &body); err != nil {
		t.Fatalf("RecordEventForDevice: %v", err)
	}

	got, events, err := r.GetWithEventsAccessible(ctx, s.ID, "op", nil, false)
	if err != nil {
		t.Fatalf("GetWithEventsAccessible: %v", err)
	}
	if got.DeviceID == nil || *got.DeviceID != deviceID {
		t.Fatalf("device_id = %v, want %s", got.DeviceID, deviceID)
	}
	if len(events) != 1 || events[0].Summary != "correlated" {
		t.Fatalf("events = %+v, want correlated event", events)
	}
}

func TestAccessibleOperationsHideForeignAndRemoteIPCaptures(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	var customerA, customerB, deviceA, deviceB string
	if err := db.QueryRowContext(ctx, `INSERT INTO customers (id, name) VALUES (gen_random_uuid(), 'A') RETURNING id`).Scan(&customerA); err != nil {
		t.Fatalf("insert customer A: %v", err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO customers (id, name) VALUES (gen_random_uuid(), 'B') RETURNING id`).Scan(&customerB); err != nil {
		t.Fatalf("insert customer B: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number, customer_id)
		VALUES (gen_random_uuid(), '001349+LOCAL', 'Vendor', '001349', 'CPE', 'LOCAL', $1)
		RETURNING id`, customerA).Scan(&deviceA); err != nil {
		t.Fatalf("insert device A: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number, customer_id)
		VALUES (gen_random_uuid(), '001349+FOREIGN', 'Vendor', '001349', 'CPE', 'FOREIGN', $1)
		RETURNING id`, customerB).Scan(&deviceB); err != nil {
		t.Fatalf("insert device B: %v", err)
	}
	s, err := r.Start(ctx, StartParams{DeviceID: &deviceB, MatchType: MatchDevice, MatchValue: "001349+FOREIGN", Protocol: "CWMP", StartedBy: "bob", MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.RecordEvent(ctx, s.ID, "inbound", "Inform", "secret", nil); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	if _, _, err := r.GetWithEventsAccessible(ctx, s.ID, "alice", []string{customerA}, true); err != ErrNotFound {
		t.Errorf("foreign read = %v, want ErrNotFound", err)
	}
	if _, err := r.StopAccessible(ctx, s.ID, "alice", []string{customerA}, true); err != ErrNotFound {
		t.Errorf("foreign stop = %v, want ErrNotFound", err)
	}
	visible, err := r.ListAccessible(ctx, "alice", []string{customerA})
	if err != nil {
		t.Fatalf("ListAccessible: %v", err)
	}
	if len(visible) != 0 {
		t.Errorf("ListAccessible leaked %+v", visible)
	}

	remote, err := r.Start(ctx, StartParams{MatchType: MatchRemoteIP, MatchValue: "192.0.2.25", Protocol: "CWMP", StartedBy: "root", MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Start remote capture: %v", err)
	}
	if err := r.RecordEventForDevice(ctx, remote.ID, deviceB, "inbound", "Inform", "device B", nil); err != nil {
		t.Fatalf("correlate remote capture: %v", err)
	}
	if err := r.RecordEventForDevice(ctx, remote.ID, deviceA, "inbound", "Inform", "device A behind same NAT", nil); err == nil {
		t.Fatal("second device behind shared IP was recorded into an already-correlated remote capture")
	}
	if err := r.RecordEventWhileUnresolved(ctx, remote.ID, "inbound", "Notify", "unknown device behind same NAT", nil); err != nil {
		t.Fatalf("skip unresolved event after correlation: %v", err)
	}
	if _, _, err := r.GetWithEventsAccessible(ctx, remote.ID, "bob", []string{customerB}, true); err != ErrNotFound {
		t.Errorf("tenant read of global remote capture = %v, want ErrNotFound", err)
	}
	if _, err := r.StopAccessible(ctx, remote.ID, "bob", []string{customerB}, true); err != ErrNotFound {
		t.Errorf("tenant stop of global remote capture = %v, want ErrNotFound", err)
	}
	visible, err = r.ListAccessible(ctx, "bob", []string{customerB})
	if err != nil {
		t.Fatalf("ListAccessible for device B tenant: %v", err)
	}
	for _, item := range visible {
		if item.ID == remote.ID {
			t.Errorf("tenant list leaked global remote capture %+v", item)
		}
	}
	_, events, err := r.GetWithEventsAccessible(ctx, remote.ID, "root", nil, false)
	if err != nil {
		t.Fatalf("global read of remote capture: %v", err)
	}
	if len(events) != 1 || events[0].Summary != "device B" {
		t.Errorf("remote capture events = %+v, want only device B", events)
	}
}

func TestListReturnsNewestFirst(t *testing.T) {
	ctx, db := newTestDB(t)
	r := NewRepository(db)

	s1, err := r.Start(ctx, StartParams{MatchType: MatchDevice, MatchValue: "001349+S6", Protocol: "CWMP", StartedBy: "op", MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Start s1: %v", err)
	}
	s2, err := r.Start(ctx, StartParams{MatchType: MatchDevice, MatchValue: "001349+S7", Protocol: "CWMP", StartedBy: "op", MaxDuration: time.Minute})
	if err != nil {
		t.Fatalf("Start s2: %v", err)
	}

	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].ID != s2.ID || list[1].ID != s1.ID {
		t.Fatalf("List() = %+v, want [s2, s1] (newest first)", list)
	}
}
