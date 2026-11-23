package connectivity

import (
	"context"
	"os"
	"testing"
	"time"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/store"
)

var t0 = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

// every returns n periodic Informs, step apart, starting at t0.
func every(step time.Duration, n int) []Checkin {
	out := make([]Checkin, n)
	for i := range out {
		out[i] = Checkin{At: t0.Add(time.Duration(i) * step), Codes: []string{"2 PERIODIC"}}
	}
	return out
}

func TestDeriveConnectivitySteadyDeviceHasNoOutage(t *testing.T) {
	cs := every(15*time.Minute, 20)
	got := DeriveConnectivity(cs, t0, cs[len(cs)-1].At.Add(10*time.Minute))
	if len(got.Outages) != 0 {
		t.Fatalf("outages = %+v, want none", got.Outages)
	}
	if got.AvailabilityPercent == nil || *got.AvailabilityPercent != 100 {
		t.Fatalf("availability = %v, want 100", got.AvailabilityPercent)
	}
	if got.IntervalSeconds != 900 {
		t.Fatalf("interval = %d, want 900", got.IntervalSeconds)
	}
}

func TestDeriveConnectivitySeparatesAConnectivityLossFromARestart(t *testing.T) {
	cs := every(15*time.Minute, 4) // t0 .. t0+45m
	// Silent for two hours, back without a restart code: the CPE stayed up.
	cs = append(cs, Checkin{At: cs[3].At.Add(2 * time.Hour), Codes: []string{"4 VALUE CHANGE"}})
	// Then silent for an hour and back with 1 BOOT: it had restarted.
	cs = append(cs, Checkin{At: cs[4].At.Add(time.Hour), Codes: []string{"1 BOOT", "2 PERIODIC"}})
	got := DeriveConnectivity(cs, t0, cs[5].At)
	if len(got.Outages) != 2 {
		t.Fatalf("outages = %+v, want 2", got.Outages)
	}
	if o := got.Outages[0]; o.Kind != OutageConnectivity || o.Seconds != 7200 || o.BackAt == nil {
		t.Errorf("first outage = %+v", o)
	}
	if o := got.Outages[1]; o.Kind != OutageRestart || o.Seconds != 3600 {
		t.Errorf("second outage = %+v", o)
	}
}

func TestDeriveConnectivityReportsAnOngoingOutage(t *testing.T) {
	cs := every(15*time.Minute, 4)
	now := cs[3].At.Add(3 * time.Hour)
	got := DeriveConnectivity(cs, t0, now)
	if len(got.Outages) != 1 || got.Outages[0].Kind != OutageOngoing || got.Outages[0].BackAt != nil {
		t.Fatalf("outages = %+v, want one ongoing", got.Outages)
	}
	if got.Outages[0].LastSeen != cs[3].At || got.Outages[0].Seconds != 10800 {
		t.Errorf("ongoing outage = %+v", got.Outages[0])
	}
	// 3h of 3h45m observed.
	if got.AvailabilityPercent == nil || *got.AvailabilityPercent < 19 || *got.AvailabilityPercent > 21 {
		t.Errorf("availability = %v, want about 20", got.AvailabilityPercent)
	}
}

func TestDeriveConnectivityHourlyReporterIsNotFlaggedForANormalHour(t *testing.T) {
	cs := every(time.Hour, 12)
	got := DeriveConnectivity(cs, t0, cs[11].At.Add(30*time.Minute))
	if len(got.Outages) != 0 {
		t.Fatalf("outages = %+v, want none for a device that reports hourly", got.Outages)
	}
	if got.ThresholdSeconds != 9000 { // 2.5 x 1h
		t.Errorf("threshold = %d, want 9000", got.ThresholdSeconds)
	}
}

func TestDeriveConnectivityNoCheckins(t *testing.T) {
	got := DeriveConnectivity(nil, t0, t0.Add(time.Hour))
	if len(got.Outages) != 0 || got.AvailabilityPercent != nil || got.Checkins != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}

// TestForDeviceReadsSessionsAndEventCodes runs the query against a real
// database: Informs are read oldest first, the event codes come back as a
// list (text[] through pgx), and another device's Informs are not mixed in.
func TestForDeviceReadsSessionsAndEventCodes(t *testing.T) {
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
	r := NewRepository(db)
	seed := func(serial string) *devices.Device {
		t.Helper()
		key := cwmp.DeviceID{OUI: "001349", ProductClass: "NR7101", SerialNumber: serial}.NaturalKey()
		d, err := devices.NewRepository(db).PreRegister(ctx, key, "", "001349", "NR7101", serial, nil, nil)
		if err != nil {
			t.Fatalf("seed device: %v", err)
		}
		return d
	}
	d := seed("CONNECTIVITY-01")
	other := seed("CONNECTIVITY-02")

	insert := func(deviceID string, at time.Time, codes ...string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO cwmp_sessions (id, device_id, state, inform_event_codes, opened_at)
			VALUES (gen_random_uuid(), $1, 'CLOSED', $2, $3)`, deviceID, store.StringArray(codes), at); err != nil {
			t.Fatalf("insert session: %v", err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	insert(d.ID, now.Add(-6*time.Hour), "2 PERIODIC")
	insert(d.ID, now.Add(-5*time.Hour-45*time.Minute), "2 PERIODIC")
	insert(d.ID, now.Add(-5*time.Hour-30*time.Minute), "2 PERIODIC")
	// Two hours of silence, back with a restart.
	insert(d.ID, now.Add(-3*time.Hour-30*time.Minute), "1 BOOT", "2 PERIODIC")
	insert(d.ID, now.Add(-3*time.Hour-15*time.Minute), "2 PERIODIC")
	insert(other.ID, now.Add(-1*time.Hour), "2 PERIODIC")

	got, err := r.ForDevice(ctx, d.ID, now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatalf("Connectivity: %v", err)
	}
	if got.Checkins != 5 {
		t.Fatalf("checkins = %d, want 5 (the other device's Inform must not count)", got.Checkins)
	}
	if len(got.Outages) != 2 {
		t.Fatalf("outages = %+v, want a restart and an ongoing one", got.Outages)
	}
	if o := got.Outages[0]; o.Kind != OutageRestart || o.Seconds != 7200 {
		t.Errorf("first outage = %+v, want a 2h restart", o)
	}
	if o := got.Outages[1]; o.Kind != OutageOngoing || o.BackAt != nil {
		t.Errorf("second outage = %+v, want ongoing", o)
	}
}
