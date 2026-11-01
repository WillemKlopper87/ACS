package onboarding

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"acs/internal/store"
)

func newTestRepo(t *testing.T) (context.Context, *Repository, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed onboarding test")
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
	return ctx, NewRepository(db), db
}

func TestRepositoryLifecycle(t *testing.T) {
	ctx, r, db := newTestRepo(t)

	site, err := r.CreateSite(ctx, Site{Name: "14 Oak Ave", City: "Embalenhle", Country: "ZA"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.CreatePlan(ctx, ServicePlan{Code: "HOME-5G-100", Name: "Home 5G 100", AllowedPLMNs: []string{"65506"},
		Cellular: CellularProfile{APN: "internet", DNN: "internet"}, RequireDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.GetPlan(ctx, plan.ID); err != nil || got.Cellular.APN != "internet" || got.AllowedPLMNs[0] != "65506" {
		t.Fatalf("plan round trip = %+v, %v", got, err)
	}

	o, err := r.Create(ctx, Onboarding{ExpectedOUI: "A4FC77", ExpectedSerial: "CSC25430018F", ExpectedICCID: "89270000000000001234",
		AccountRef: "ACC-77", SiteID: &site.ID, ServicePlanID: &plan.ID, Source: "console", RequestedBy: "noc-1"})
	if err != nil {
		t.Fatal(err)
	}
	if o.State != StatePlanned || o.Role != "gateway" || o.CommercialModel != "retail" {
		t.Fatalf("created = %+v", o)
	}
	if _, err := r.Create(ctx, Onboarding{ExpectedSerial: "CSC25430018F", Source: "bss"}); !errors.Is(err, ErrOpenOnboardingExists) {
		t.Fatalf("second open onboarding for the serial: %v, want ErrOpenOnboardingExists", err)
	}

	// The device appears: matched by OUI + serial before it has a device id.
	var deviceID string
	if err := db.QueryRowContext(ctx, `INSERT INTO devices (id, oui_serial, oui, serial_number) VALUES (gen_random_uuid(), 'A4FC77+3TG02241+CSC25430018F', 'A4FC77', 'CSC25430018F') RETURNING id`).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	if u, err := r.Unclaimed(ctx, 10); err != nil || len(u) != 0 {
		t.Fatalf("a planned device listed as unclaimed: %+v, %v", u, err)
	}
	found, err := r.OpenForDevice(ctx, deviceID, "A4FC77", "CSC25430018F")
	if err != nil || found.ID != o.ID {
		t.Fatalf("match on first contact = %+v, %v", found, err)
	}
	if _, err := r.OpenForDevice(ctx, deviceID, "000000", "CSC25430018F"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other OUI matched: %v", err)
	}

	moved, err := r.Apply(ctx, *found, Decision{State: StateContacted}, deviceID, nil)
	if err != nil || !moved {
		t.Fatalf("apply CONTACTED: %v %v", moved, err)
	}
	// A second gateway acting on the old state must not move it again.
	if moved, err := r.Apply(ctx, *found, Decision{State: StateContacted}, deviceID, nil); err != nil || moved {
		t.Fatalf("stale apply moved: %v %v", moved, err)
	}
	got, _ := r.Get(ctx, o.ID)
	if got.State != StateContacted || got.DeviceID == nil || *got.DeviceID != deviceID || got.ContactedAt == nil {
		t.Fatalf("after contact = %+v", got)
	}
	var siteOnDevice sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT site_id FROM devices WHERE id=$1`, deviceID).Scan(&siteOnDevice); err != nil || siteOnDevice.String != site.ID {
		t.Fatalf("device site = %v, %v; want the onboarding's site", siteOnDevice, err)
	}

	// Configuration jobs and their outcome.
	if _, err := db.ExecContext(ctx, `INSERT INTO jobs (id, command_key, device_id, type, status, payload) VALUES
		(gen_random_uuid(), 'set_a', $1, 'SET_PARAMETER', 'SUCCESS', '{}'),
		(gen_random_uuid(), 'set_b', $1, 'SET_PARAMETER', 'FAILED', '{}')`, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET fault_string = 'fault 9007' WHERE command_key = 'set_b'`); err != nil {
		t.Fatal(err)
	}
	pending, done, failed, firstErr, err := r.ConfigOutcome(ctx, []string{"set_a", "set_b", "set_missing"})
	if err != nil || pending != 1 || done != 1 || failed != 1 || firstErr != "fault 9007" {
		t.Fatalf("outcome = %d/%d/%d %q %v", pending, done, failed, firstErr, err)
	}

	// Retry and cancel.
	got.State = StateContacted
	if _, err := r.Apply(ctx, *got, Decision{State: StateOnHold, Reason: "SIM does not match the sale"}, deviceID, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Retry(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = r.Get(ctx, o.ID); got.State != StateContacted || got.StateReason != "" {
		t.Fatalf("after retry = %+v", got)
	}
	if err := r.Cancel(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Cancel(ctx, o.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel of a closed onboarding: %v", err)
	}
	// Closed: the serial can be planned again, and the device is unclaimed.
	if u, err := r.Unclaimed(ctx, 10); err != nil || len(u) != 0 {
		t.Fatalf("a device with an onboarding history is unclaimed: %+v %v", u, err)
	}
	if _, err := r.Create(ctx, Onboarding{ExpectedSerial: "CSC25430018F", Source: "console"}); err != nil {
		t.Fatalf("re-plan after cancel: %v", err)
	}
	if list, err := r.List(ctx, ListFilter{State: StatePlanned}); err != nil || len(list) != 1 {
		t.Fatalf("planned list = %d, %v", len(list), err)
	}
}

func TestUnclaimedLists_DevicesNobodyOnboarded(t *testing.T) {
	ctx, r, db := newTestRepo(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial, manufacturer, product_class, serial_number, last_inform_at)
		VALUES (gen_random_uuid(), 'X+Y+STRAY1', 'Huawei', 'HG8245', 'STRAY1', now())`); err != nil {
		t.Fatal(err)
	}
	u, err := r.Unclaimed(ctx, 10)
	if err != nil || len(u) != 1 || u[0].SerialNumber != "STRAY1" || u[0].Manufacturer != "Huawei" {
		t.Fatalf("unclaimed = %+v, %v", u, err)
	}
}
