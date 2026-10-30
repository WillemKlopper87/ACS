package wifiinsights

import (
	"context"
	"os"
	"testing"
	"time"

	"acs/internal/store"
)

func newInsightsTestRepo(t *testing.T) (context.Context, *Repository, string) {
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
	const deviceID = "22222222-2222-2222-2222-222222222222"
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ($1, 'A4FC77-CSC25430018F')`, deviceID); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	return ctx, NewRepository(db, nil), deviceID
}

func i64(v int64) *int64 { return &v }

// TestRecordAndOverview drives three samples through Record and checks the
// derived bandwidth (including a counter reset), top lists, client counts
// and session bookkeeping.
func TestRecordAndOverview(t *testing.T) {
	ctx, r, deviceID := newInsightsTestRepo(t)
	t0 := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	phone, laptop := "ca:76:47:8a:17:87", "ec:74:d7:1e:89:84"

	samples := []struct {
		at    time.Time
		snap  Snapshot
		label string
	}{
		{t0, Snapshot{
			SSIDs:   []SSIDCounter{{Instance: 1, Name: "SENT-5G", Role: "primary", BytesDown: 1000, BytesUp: 100}},
			Clients: []Client{{MAC: phone, Hostname: "Galaxy-A12", Wireless: true, SSID: 1, BytesDown: i64(500), BytesUp: i64(50)}, {MAC: laptop}},
		}, "baseline"},
		{t0.Add(5 * time.Minute), Snapshot{
			SSIDs:   []SSIDCounter{{Instance: 1, Name: "SENT-5G", Role: "primary", BytesDown: 4000, BytesUp: 400}},
			Clients: []Client{{MAC: phone, Wireless: true, SSID: 1, BytesDown: i64(2500), BytesUp: i64(250)}, {MAC: laptop}},
		}, "+3000/+300"},
		// CPE rebooted: counters restart, the negative difference is dropped.
		{t0.Add(10 * time.Minute), Snapshot{
			SSIDs:   []SSIDCounter{{Instance: 1, Name: "SENT-5G", Role: "primary", BytesDown: 200, BytesUp: 20}},
			Clients: []Client{{MAC: phone, Wireless: true, SSID: 1, BytesDown: i64(100), BytesUp: i64(10)}},
		}, "reset"},
	}
	for _, s := range samples {
		if err := r.Record(ctx, deviceID, s.at, s.snap, 15*time.Minute); err != nil {
			t.Fatalf("record %s: %v", s.label, err)
		}
	}
	// Re-recording the same instant is a no-op.
	if err := r.Record(ctx, deviceID, samples[0].at, samples[0].snap, 15*time.Minute); err != nil {
		t.Fatal(err)
	}

	ov, err := r.Overview(ctx, deviceID, Windows["2h"], time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if ov.TotalDown != 3000 || ov.TotalUp != 300 {
		t.Fatalf("totals = %d/%d, want 3000/300", ov.TotalDown, ov.TotalUp)
	}
	if len(ov.TopSSIDs) != 1 || ov.TopSSIDs[0].Name != "SENT-5G" || ov.TopSSIDs[0].BytesDown != 3000 {
		t.Fatalf("top ssids = %+v", ov.TopSSIDs)
	}
	if len(ov.TopClients) != 1 || ov.TopClients[0].MAC != phone || ov.TopClients[0].BytesDown != 2000 || ov.TopClients[0].Hostname != "Galaxy-A12" {
		t.Fatalf("top clients = %+v", ov.TopClients)
	}
	if len(ov.ClientCounts) == 0 {
		t.Fatal("no client counts")
	}
	if ov.LastSampleAt == nil || !ov.LastSampleAt.Equal(samples[2].at) {
		t.Fatalf("last sample = %v", ov.LastSampleAt)
	}
	if len(ov.Clients) != 2 || ov.ClientStats.New != 2 || ov.ClientStats.Private != 1 {
		t.Fatalf("clients = %+v stats = %+v", ov.Clients, ov.ClientStats)
	}
	for _, c := range ov.Clients {
		switch c.MAC {
		case phone:
			if !c.Connected || c.Sessions != 1 || c.ConnectedSeconds != 600 || c.OSInferred != "Android" || c.Vendor != "Private" {
				t.Errorf("phone = %+v", c)
			}
		case laptop:
			if c.Connected || c.ConnectedSeconds != 300 || c.Wireless {
				t.Errorf("laptop = %+v", c)
			}
		}
	}

	// Back after longer than the session gap: a new session, no time added
	// for the absence.
	if err := r.Record(ctx, deviceID, samples[2].at.Add(time.Hour), Snapshot{Clients: []Client{{MAC: laptop}}}, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	ov, err = r.Overview(ctx, deviceID, Windows["1d"], time.Now().UTC().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range ov.Clients {
		if c.MAC == laptop && (c.Sessions != 2 || c.ConnectedSeconds != 300) {
			t.Errorf("laptop after gap = %+v", c)
		}
	}
}
