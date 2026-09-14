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
