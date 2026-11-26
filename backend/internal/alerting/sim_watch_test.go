package alerting

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"acs/internal/store"
)

func TestClassifySIMChangeDefaults(t *testing.T) {
	var p Policy
	for code, want := range map[string]Priority{"sim_change_alert": P2, "sim_change_notice": P4, "something_else": P3} {
		if got := Classify(p, code, false); got != want {
			t.Errorf("Classify(%q) = %s, want %s", code, got, want)
		}
	}
	p.FaultPriorities = map[string]Priority{"sim_change_alert": P1, "sim_change_*": P3}
	if got := Classify(p, "sim_change_alert", false); got != P1 {
		t.Errorf("explicit mapping = %s, want P1", got)
	}
	if got := Classify(p, "sim_change_notice", false); got != P3 {
		t.Errorf("wildcard mapping = %s, want P3", got)
	}
}

func simWatchDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed alerting test")
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

func simDevice(t *testing.T, ctx context.Context, db *sql.DB, serial, account string) string {
	t.Helper()
	var id string
	if err := db.QueryRowContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES (gen_random_uuid(), $1) RETURNING id::text`, "AABBCC-"+serial).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if account != "" {
		var key string
		err := db.QueryRowContext(ctx, `SELECT id::text FROM bss_accounts WHERE account_id = $1`, account).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			err = db.QueryRowContext(ctx, `INSERT INTO bss_accounts (account_id, claimed_by) VALUES ($1, 'test') RETURNING id::text`, account).Scan(&key)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO account_device_mappings (id, account_id, account_key, device_id, oui_serial) VALUES (gen_random_uuid(), $1, $2::uuid, $3, $4)`, account, key, id, "AABBCC-"+serial); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func simRow(t *testing.T, ctx context.Context, db *sql.DB, deviceID, state string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO device_sim_watch (device_id, state, network, iccid_hint, imsi_hint, detected_at)
		VALUES ($1, $2, '65501', '…0022', '65501…0022', now())
		ON CONFLICT (device_id) DO UPDATE SET state = EXCLUDED.state`, deviceID, state); err != nil {
		t.Fatal(err)
	}
}

func TestSIMWatchDevicesListsOnlyOpenMappedDevices(t *testing.T) {
	ctx, db := simWatchDB(t)
	mapped := simDevice(t, ctx, db, "S1", "acct-1")
	okDev := simDevice(t, ctx, db, "S2", "acct-2")
	unmapped := simDevice(t, ctx, db, "S3", "")
	unassigned := simDevice(t, ctx, db, "S4", "acct-4")
	if _, err := db.ExecContext(ctx, `UPDATE account_device_mappings SET unassigned_at = now() WHERE device_id = $1`, unassigned); err != nil {
		t.Fatal(err)
	}
	simRow(t, ctx, db, mapped, "alert")
	simRow(t, ctx, db, okDev, "ok")
	simRow(t, ctx, db, unmapped, "notice")
	simRow(t, ctx, db, unassigned, "notice")

	got, err := NewRepository(db).SIMWatchDevices(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DeviceID != mapped || got[0].TenantID != "acct-1" || got[0].State != "alert" || got[0].Network != "65501" || got[0].IMSIHint != "65501…0022" {
		t.Fatalf("got %+v, want only the mapped device in alert", got)
	}
}

func TestRecoverSIMWatchAndByKey(t *testing.T) {
	ctx, db := simWatchDB(t)
	dev := simDevice(t, ctx, db, "R1", "acct-1")
	inc := NewIncidentRepository(db)
	if _, err := inc.ByKey(ctx, "acct-1", dev, SIMChangeConditionKey); !errors.Is(err, ErrIncidentNotFound) {
		t.Fatalf("ByKey before opening = %v, want ErrIncidentNotFound", err)
	}
	opened, created, err := inc.OpenWithCreated(ctx, "acct-1", dev, SIMChangeConditionKey, P4, "SIM replaced", nil, map[string]any{})
	if err != nil || !created {
		t.Fatalf("open: %v %v", created, err)
	}
	if got, err := inc.ByKey(ctx, "acct-1", dev, SIMChangeConditionKey); err != nil || got.ID != opened.ID || got.State != IncidentOpen {
		t.Fatalf("ByKey = %+v, %v", got, err)
	}
	// An incident of another kind on the same device is never touched.
	if _, _, err := inc.OpenWithCreated(ctx, "acct-1", dev, "acs-offline", P2, "offline", nil, map[string]any{}); err != nil {
		t.Fatal(err)
	}

	simRow(t, ctx, db, dev, "notice")
	if got, err := inc.RecoverSIMWatch(ctx); err != nil || len(got) != 0 {
		t.Fatalf("recovered while still open: %+v, %v", got, err)
	}
	simRow(t, ctx, db, dev, "ok")
	got, err := inc.RecoverSIMWatch(ctx)
	if err != nil || len(got) != 1 || got[0].ID != opened.ID || got[0].State != IncidentRecovered {
		t.Fatalf("recovered = %+v, %v", got, err)
	}
	var other string
	if err := db.QueryRowContext(ctx, `SELECT state FROM alert_incidents WHERE condition_key='acs-offline'`).Scan(&other); err != nil || other != "open" {
		t.Fatalf("offline incident state = %q, %v; want open", other, err)
	}
	if again, err := inc.RecoverSIMWatch(ctx); err != nil || len(again) != 0 {
		t.Fatalf("second recovery = %+v, %v", again, err)
	}
}

func TestRecoverSIMWatchWhenTheRowIsGone(t *testing.T) {
	ctx, db := simWatchDB(t)
	dev := simDevice(t, ctx, db, "R2", "acct-1")
	inc := NewIncidentRepository(db)
	if _, _, err := inc.OpenWithCreated(ctx, "acct-1", dev, SIMChangeConditionKey, P4, "SIM replaced", nil, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	got, err := inc.RecoverSIMWatch(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("an incident whose device has no open verdict must recover: %+v, %v", got, err)
	}
}

// More open verdicts than one batch must all be reachable, alerts first, so
// a pile of old unapproved notices cannot starve a newer swap.
func TestSIMWatchDevicesPagesThroughEveryOpenVerdict(t *testing.T) {
	ctx, db := simWatchDB(t)
	var notices []string
	for i, serial := range []string{"P1", "P2", "P3"} {
		d := simDevice(t, ctx, db, serial, "acct-p"+string(rune('1'+i)))
		simRow(t, ctx, db, d, "notice")
		notices = append(notices, d)
	}
	alertDev := simDevice(t, ctx, db, "P4", "acct-p4")
	simRow(t, ctx, db, alertDev, "alert")
	if _, err := db.ExecContext(ctx, `UPDATE device_sim_watch SET detected_at = now() WHERE device_id = $1`, alertDev); err != nil {
		t.Fatal(err)
	}
	r := NewRepository(db)
	var seen []string
	for offset := 0; ; offset += 2 {
		page, err := r.SIMWatchDevices(ctx, 2, offset)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range page {
			seen = append(seen, d.DeviceID)
		}
		if len(page) < 2 {
			break
		}
	}
	if len(seen) != 4 || seen[0] != alertDev {
		t.Fatalf("seen = %v, want 4 devices with the alert first (%s)", seen, alertDev)
	}
	for _, n := range notices {
		found := false
		for _, id := range seen {
			found = found || id == n
		}
		if !found {
			t.Fatalf("notice %s never listed: %v", n, seen)
		}
	}
}

// A device taken off its account no longer belongs to the tenant that was
// told about the SIM change; its incident must not stay open and escalate.
func TestRecoverSIMWatchWhenTheDeviceWasUnassigned(t *testing.T) {
	ctx, db := simWatchDB(t)
	dev := simDevice(t, ctx, db, "U1", "acct-u")
	inc := NewIncidentRepository(db)
	simRow(t, ctx, db, dev, "notice")
	if _, _, err := inc.OpenWithCreated(ctx, "acct-u", dev, SIMChangeConditionKey, P4, "SIM replaced", nil, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got, err := inc.RecoverSIMWatch(ctx); err != nil || len(got) != 0 {
		t.Fatalf("a mapped device with an open verdict must stay open: %+v, %v", got, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE account_device_mappings SET unassigned_at = now() WHERE device_id = $1`, dev); err != nil {
		t.Fatal(err)
	}
	got, err := inc.RecoverSIMWatch(ctx)
	if err != nil || len(got) != 1 || got[0].State != IncidentRecovered {
		t.Fatalf("an unassigned device's incident must recover: %+v, %v", got, err)
	}
}
