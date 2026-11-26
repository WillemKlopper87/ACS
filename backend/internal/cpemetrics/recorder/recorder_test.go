package recorder_test

import (
	"context"
	"os"
	"testing"
	"time"

	"acs/internal/cpemetrics"
	"acs/internal/cpemetrics/recorder"
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/store"
)

func TestRecordIgnoresADuplicateSample(t *testing.T) {
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
	key := cwmp.DeviceID{OUI: "001349", ProductClass: "NR7101", SerialNumber: "CPE-REC-01"}.NaturalKey()
	d, err := devices.NewRepository(db).PreRegister(ctx, key, "", "001349", "NR7101", "CPE-REC-01", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsrp := -95.0
	at := time.Now().UTC().Truncate(time.Second)
	rec := recorder.New(db)
	for i := 0; i < 2; i++ {
		if err := rec.Record(ctx, d.ID, at, cpemetrics.Sample{RSRP: &rsrp, Band: "B3"}); err != nil {
			t.Fatalf("record #%d: %v", i+1, err)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM device_cpe_samples WHERE device_id=$1`, d.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d, %v; want 1", n, err)
	}
}
