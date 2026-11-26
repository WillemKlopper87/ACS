package recorder_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/simwatch"
	"acs/internal/simwatch/recorder"
	"acs/internal/store"
)

const (
	iccidA = "8927000000000000011"
	iccidB = "8927000000000000022"
	imsiA  = "655010000000011"
	imsiB  = "655010000000022"
)

func setup(t *testing.T) (context.Context, *sql.DB, *recorder.Repository, string) {
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
	key := cwmp.DeviceID{OUI: "001349", ProductClass: "NR7101", SerialNumber: "CPE-SIM-01"}.NaturalKey()
	d, err := devices.NewRepository(db).PreRegister(ctx, key, "", "001349", "NR7101", "CPE-SIM-01", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, db, recorder.New(db), d.ID
}

type row struct {
	state, network, iccidHint, imsiHint, prevICCID, prevIMSI string
	detectedAt, checkedAt, approvedAt                         sql.NullTime
}

func get(t *testing.T, ctx context.Context, db *sql.DB, id string) (row, bool) {
	t.Helper()
	var r row
	err := db.QueryRowContext(ctx, `SELECT state, network, iccid_hint, imsi_hint, previous_iccid_hint, previous_imsi_hint,
		detected_at, last_checked_at, approved_at FROM device_sim_watch WHERE device_id=$1`, id).
		Scan(&r.state, &r.network, &r.iccidHint, &r.imsiHint, &r.prevICCID, &r.prevIMSI, &r.detectedAt, &r.checkedAt, &r.approvedAt)
	if err == sql.ErrNoRows {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

func notice(network string) simwatch.Verdict {
	return simwatch.Verdict{Kind: simwatch.KindNotice, Network: network}
}

func TestBaselineCreatesAnOKRow(t *testing.T) {
	ctx, db, rec, id := setup(t)
	at := time.Now().UTC().Truncate(time.Second)
	if err := rec.Record(ctx, id, simwatch.Verdict{Kind: simwatch.KindBaseline, Network: "65501"}, simwatch.Identity{ICCID: iccidA, IMSI: imsiA}, simwatch.Identity{}, at); err != nil {
		t.Fatal(err)
	}
	r, ok := get(t, ctx, db, id)
	if !ok || r.state != "ok" || r.network != "65501" || r.iccidHint == "" || r.detectedAt.Valid {
		t.Fatalf("row = %+v, %v", r, ok)
	}
}

func TestIdenticalRepeatKeepsDetectedAtAndAlertUpgradesState(t *testing.T) {
	ctx, db, rec, id := setup(t)
	cur, prev := simwatch.Identity{ICCID: iccidB, IMSI: imsiB}, simwatch.Identity{ICCID: iccidA, IMSI: imsiA}
	first := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	if err := rec.Record(ctx, id, notice("65501"), cur, prev, first); err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(ctx, id, notice("65501"), cur, prev, first.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	r, _ := get(t, ctx, db, id)
	if r.state != "notice" || !r.detectedAt.Time.Equal(first) {
		t.Fatalf("repeat moved detected_at: %+v (want %v)", r, first)
	}
	if !r.checkedAt.Time.Equal(first.Add(10 * time.Minute)) {
		t.Fatalf("last_checked_at not refreshed: %+v", r)
	}
	if r.prevICCID == "" || r.prevIMSI == "" {
		t.Fatalf("previous hints missing: %+v", r)
	}
	alert := simwatch.Verdict{Kind: simwatch.KindAlert, Network: "65501"}
	if err := rec.Record(ctx, id, alert, cur, prev, first.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if r, _ = get(t, ctx, db, id); r.state != "alert" || !r.detectedAt.Time.Equal(first.Add(20*time.Minute)) {
		t.Fatalf("alert did not replace notice: %+v", r)
	}
}

func TestANewVerdictClearsTheApproval(t *testing.T) {
	ctx, db, rec, id := setup(t)
	at := time.Now().UTC().Truncate(time.Second)
	if err := rec.Record(ctx, id, notice("65501"), simwatch.Identity{ICCID: iccidB, IMSI: imsiB}, simwatch.Identity{ICCID: iccidA, IMSI: imsiA}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE device_sim_watch SET state='ok', approved_at=now(), approved_by='op', approval_reason='checked on site' WHERE device_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := rec.Record(ctx, id, notice("65501"), simwatch.Identity{ICCID: iccidA, IMSI: imsiA}, simwatch.Identity{ICCID: iccidB, IMSI: imsiB}, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, _ := get(t, ctx, db, id)
	if r.state != "notice" || r.approvedAt.Valid {
		t.Fatalf("approval not cleared: %+v", r)
	}
}

func TestNoneOnlyTouchesAnExistingRow(t *testing.T) {
	ctx, db, rec, id := setup(t)
	at := time.Now().UTC().Truncate(time.Second)
	if err := rec.Record(ctx, id, simwatch.Verdict{Kind: simwatch.KindNone}, simwatch.Identity{ICCID: iccidA}, simwatch.Identity{ICCID: iccidA}, at); err != nil {
		t.Fatal(err)
	}
	if _, ok := get(t, ctx, db, id); ok {
		t.Fatal("a None verdict must not create a row")
	}
	if last, err := rec.LastChecked(ctx, id); err != nil || !last.IsZero() {
		t.Fatalf("LastChecked without a row = %v, %v", last, err)
	}
	if err := rec.Record(ctx, id, simwatch.Verdict{Kind: simwatch.KindBaseline}, simwatch.Identity{ICCID: iccidA}, simwatch.Identity{}, at); err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Hour)
	if err := rec.Record(ctx, id, simwatch.Verdict{Kind: simwatch.KindNone}, simwatch.Identity{ICCID: iccidA}, simwatch.Identity{ICCID: iccidA}, later); err != nil {
		t.Fatal(err)
	}
	if last, err := rec.LastChecked(ctx, id); err != nil || !last.Equal(later) {
		t.Fatalf("LastChecked = %v, %v; want %v", last, err, later)
	}
}

func TestStoredTextNeverHoldsAFullIdentifier(t *testing.T) {
	ctx, db, rec, id := setup(t)
	if err := rec.Record(ctx, id, notice("65501"), simwatch.Identity{ICCID: iccidB, IMSI: imsiB}, simwatch.Identity{ICCID: iccidA, IMSI: imsiA}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var all string
	if err := db.QueryRowContext(ctx, `SELECT row_to_json(w)::text FROM device_sim_watch w WHERE device_id=$1`, id).Scan(&all); err != nil {
		t.Fatal(err)
	}
	for _, full := range []string{iccidA, iccidB, imsiA, imsiB} {
		if strings.Contains(all, full) {
			t.Fatalf("row holds a full identifier %q: %s", full, all)
		}
	}
}
