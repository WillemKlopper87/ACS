package cpemetrics_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"acs/internal/cpemetrics"
	"acs/internal/cpemetrics/recorder"
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/store"
)

func f(v float64) *float64 { return &v }

func setup(t *testing.T) (context.Context, *sql.DB, func(serial string) string) {
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
	seed := func(serial string) string {
		t.Helper()
		key := cwmp.DeviceID{OUI: "001349", ProductClass: "NR7101", SerialNumber: serial}.NaturalKey()
		d, err := devices.NewRepository(db).PreRegister(ctx, key, "", "001349", "NR7101", serial, nil, nil)
		if err != nil {
			t.Fatalf("seed device: %v", err)
		}
		return d.ID
	}
	return ctx, db, seed
}

func TestHistoryReturnsRowsInOrderAndNullsStayNull(t *testing.T) {
	ctx, db, seed := setup(t)
	dev := seed("CPE-HIST-01")
	rec := recorder.New(db)
	now := time.Now().UTC().Truncate(time.Second)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Inserted out of order on purpose.
	must(rec.Record(ctx, dev, now.Add(-1*time.Hour), cpemetrics.Sample{RSRP: f(-100), RSRQ: f(-12), SNR: f(8), RSSI: f(-70), Band: "B3", RAT: "LTE"}))
	must(rec.Record(ctx, dev, now.Add(-3*time.Hour), cpemetrics.Sample{RSRP: f(-90), RSRQ: f(-10), SNR: f(15), RSSI: f(-60), Band: "B1", RAT: "LTE"}))
	must(rec.Record(ctx, dev, now.Add(-2*time.Hour), cpemetrics.Sample{RSRP: f(-95), RSRQ: f(-11), SNR: nil, RSSI: f(-65)}))

	h, err := cpemetrics.NewRepository(db).History(ctx, dev, now.Add(-24*time.Hour), now, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(h.Samples))
	}
	for i := 1; i < len(h.Samples); i++ {
		if !h.Samples[i].At.After(h.Samples[i-1].At) {
			t.Fatalf("not in order: %+v", h.Samples)
		}
	}
	if h.Samples[0].SNR == nil || *h.Samples[0].SNR != 15 || h.Samples[0].Band != "B1" {
		t.Errorf("first = %+v", h.Samples[0])
	}
	mid := h.Samples[1]
	if mid.SNR != nil {
		t.Errorf("nil SNR came back as %v", *mid.SNR)
	}
	if mid.RSRP == nil || *mid.RSRP != -95 || mid.Band != "" || mid.RAT != "" {
		t.Errorf("middle = %+v", mid)
	}
	if h.BucketSeconds != 0 || h.IntervalSeconds != 3600 {
		t.Errorf("bucket=%d interval=%d, want 0 and 3600", h.BucketSeconds, h.IntervalSeconds)
	}
	repo := cpemetrics.NewRepository(db)
	last, err := repo.LastSampleAt(ctx, dev)
	if err != nil || !last.Equal(now.Add(-1*time.Hour)) {
		t.Errorf("LastSampleAt = %v, %v", last, err)
	}
	none, err := repo.LastSampleAt(ctx, seed("CPE-HIST-NONE"))
	if err != nil || !none.IsZero() {
		t.Errorf("LastSampleAt with no rows = %v, %v", none, err)
	}
}

func TestHistoryAveragesIntoAtMostMaxPoints(t *testing.T) {
	ctx, db, seed := setup(t)
	dev := seed("CPE-HIST-02")
	rec := recorder.New(db)
	to := time.Now().UTC().Truncate(time.Second)
	from := to.Add(-30 * 24 * time.Hour)
	const n = 1200
	step := to.Sub(from) / n
	for i := 0; i < n; i++ {
		band := ""
		if i%2 == 0 {
			band = fmt.Sprintf("B%d", 1+i%7)
		}
		s := cpemetrics.Sample{RSRP: f(-120 + float64(i%40)), SNR: f(float64(i % 30)), Band: band}
		if err := rec.Record(ctx, dev, from.Add(time.Duration(i)*step+time.Second), s); err != nil {
			t.Fatal(err)
		}
	}
	h, err := cpemetrics.NewRepository(db).History(ctx, dev, from, to, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Samples) == 0 || len(h.Samples) > 500 {
		t.Fatalf("points = %d, want 1..500", len(h.Samples))
	}
	if h.BucketSeconds <= 0 {
		t.Fatalf("BucketSeconds = %d", h.BucketSeconds)
	}
	sawBand := false
	for _, p := range h.Samples {
		if p.RSRP == nil || *p.RSRP < -120 || *p.RSRP > -81 {
			t.Fatalf("averaged RSRP out of input range: %+v", p)
		}
		if p.SNR == nil || *p.SNR < 0 || *p.SNR > 29 {
			t.Fatalf("averaged SNR out of input range: %+v", p)
		}
		if p.RSRQ != nil {
			t.Fatalf("all-NULL RSRQ must stay nil: %+v", p)
		}
		if p.Band != "" {
			sawBand = true
		}
	}
	if !sawBand {
		t.Error("no bucket kept a band")
	}
}

func TestHistoryExcludesOtherDevicesAndOutOfRange(t *testing.T) {
	ctx, db, seed := setup(t)
	dev, other := seed("CPE-HIST-03"), seed("CPE-HIST-04")
	rec := recorder.New(db)
	now := time.Now().UTC().Truncate(time.Second)
	for _, c := range []struct {
		dev string
		at  time.Time
	}{
		{dev, now.Add(-1 * time.Hour)},
		{dev, now.Add(-48 * time.Hour)}, // before the window
		{other, now.Add(-1 * time.Hour)},
	} {
		if err := rec.Record(ctx, c.dev, c.at, cpemetrics.Sample{RSRP: f(-90)}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := cpemetrics.NewRepository(db).History(ctx, dev, now.Add(-24*time.Hour), now, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Samples) != 1 || !h.Samples[0].At.Equal(now.Add(-1*time.Hour)) {
		t.Fatalf("samples = %+v, want only the in-range one of this device", h.Samples)
	}
}
