package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"acs/internal/bss"
	"acs/internal/credentials"
	"acs/internal/jobs"
	"acs/internal/onboarding"
	"acs/internal/parameters"
	"acs/internal/store"
	"acs/internal/templates"
)

// testDB is a second connection to the gateway test's database, for
// arranging state the gateway reads.
func testDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db, err := store.Open(ctx, os.Getenv("ACS_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestIntegration_OnboardingFollowsInforms drives one sold CPE from PLANNED
// to IN_SERVICE through real Informs: first contact assigns the sale's site,
// the SIM is read and checked, and the plan's APN is queued only once the
// device is on its own credential and discovered.
func TestIntegration_OnboardingFollowsInforms(t *testing.T) {
	h, srv, ctx := newTestGateway(t)
	db := testDB(t, ctx)
	h.onboarding = onboarding.NewRepository(db)
	creds, err := credentials.NewRepository(db, "")
	if err != nil {
		t.Fatal(err)
	}
	h.credentials = creds

	site, err := h.onboarding.CreateSite(ctx, onboarding.Site{Name: "14 Oak Ave", Country: "ZA"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := h.onboarding.CreatePlan(ctx, onboarding.ServicePlan{Code: "HOME-5G", Name: "Home 5G",
		AllowedPLMNs: []string{"65506"}, Cellular: onboarding.CellularProfile{APN: "sales.apn"}})
	if err != nil {
		t.Fatal(err)
	}
	o, err := h.onboarding.Create(ctx, onboarding.Onboarding{ExpectedOUI: "A4FC77", ExpectedSerial: "OB0001",
		SiteID: &site.ID, ServicePlanID: &plan.ID, Source: "console"})
	if err != nil {
		t.Fatal(err)
	}
	inform := func() {
		t.Helper()
		cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
		if code, body := cpe.post(vendorInform(t, "Nokia", "A4FC77", "3TG02241", "OB0001")); code != 200 || !strings.Contains(body, "InformResponse") {
			t.Fatalf("Inform = %d %s", code, body)
		}
	}
	state := func(want string) *onboarding.Onboarding {
		t.Helper()
		got, err := h.onboarding.Get(ctx, o.ID)
		if err != nil || got.State != want {
			t.Fatalf("state = %+v, %v; want %s", got, err, want)
		}
		return got
	}

	// First contact: bound, site assigned, SIM read queued, waiting on it.
	inform()
	got := state(onboarding.StateContacted)
	if got.DeviceID == nil {
		t.Fatal("device not bound")
	}
	deviceID := *got.DeviceID
	var deviceSite sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT site_id FROM devices WHERE id = $1`, deviceID).Scan(&deviceSite); err != nil || deviceSite.String != site.ID {
		t.Fatalf("device site = %v, %v; want the sale's site", deviceSite, err)
	}
	if open, _ := h.jobs.HasOpenJobByCreator(ctx, deviceID, "onboarding:"+o.ID+":sim"); !open {
		t.Fatal("no SIM read queued")
	}
	inform() // still open: not queued twice
	var simReads int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE created_by = $1`, "onboarding:"+o.ID+":sim").Scan(&simReads); err != nil || simReads != 1 {
		t.Fatalf("SIM reads = %d, %v; want 1", simReads, err)
	}

	// The SIM reads back on the right network, the device has its own
	// credential and a discovered model: the plan's APN is queued.
	if err := h.params.Upsert(ctx, deviceID, map[string]parameters.CachedValue{
		"Device.Cellular.Interface.1.USIM.IMSI": {Value: "655060000000013"},
		"Device.Cellular.AccessPoint.1.APN":     {Value: "internet"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.params.SaveNames(ctx, deviceID, map[string]bool{
		"Device.Cellular.Interface.1.USIM.IMSI": false, "Device.Cellular.AccessPoint.1.APN": true,
	}); err != nil {
		t.Fatal(err)
	}
	cred, err := creds.Create(ctx, deviceID, credentials.TypeCWMPDigest, "ob-user", "ob-pass-0123456789", "ck")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Activate(ctx, cred.ID); err != nil {
		t.Fatal(err)
	}
	inform()
	got = state(onboarding.StateConfiguring)
	if len(got.ConfigJobs) != 1 {
		t.Fatalf("config jobs = %v, want the APN write", got.ConfigJobs)
	}
	job, err := h.jobs.ByCommandKey(ctx, got.ConfigJobs[0])
	if err != nil || job.Type != jobs.TypeSetParameter || !strings.Contains(string(job.Payload), "sales.apn") {
		t.Fatalf("config job = %+v, %v", job, err)
	}

	// Verified on the CPE: in service.
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET status = 'SUCCESS' WHERE command_key = $1`, got.ConfigJobs[0]); err != nil {
		t.Fatal(err)
	}
	inform()
	if got = state(onboarding.StateInService); got.InServiceAt == nil {
		t.Fatal("in_service_at not set")
	}
	var audits int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE action = 'OnboardingStateChanged' AND device_id = $1`, deviceID).Scan(&audits); err != nil || audits < 3 {
		t.Fatalf("state change audits = %d, %v", audits, err)
	}
}

// TestIntegration_OnboardingHoldsAForeignSIM proves a CPE whose SIM is on a
// network the plan does not allow is held, with no configuration queued.
func TestIntegration_OnboardingHoldsAForeignSIM(t *testing.T) {
	h, srv, ctx := newTestGateway(t)
	db := testDB(t, ctx)
	h.onboarding = onboarding.NewRepository(db)
	h.tmfEvents = bss.NewRepository(db)
	plan, err := h.onboarding.CreatePlan(ctx, onboarding.ServicePlan{Code: "P", Name: "P", AllowedPLMNs: []string{"65506"}})
	if err != nil {
		t.Fatal(err)
	}
	o, err := h.onboarding.Create(ctx, onboarding.Onboarding{ExpectedSerial: "OB0002", ServicePlanID: &plan.ID, Source: "bss", AccountRef: "ACC-2"})
	if err != nil {
		t.Fatal(err)
	}
	cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
	cpe.post(vendorInform(t, "Nokia", "A4FC77", "3TG02241", "OB0002"))
	got, _ := h.onboarding.Get(ctx, o.ID)
	if err := h.params.Upsert(ctx, *got.DeviceID, map[string]parameters.CachedValue{"Device.Cellular.Interface.1.USIM.IMSI": {Value: "655100000000013"}}); err != nil {
		t.Fatal(err)
	}
	cpe = &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
	cpe.post(vendorInform(t, "Nokia", "A4FC77", "3TG02241", "OB0002"))
	got, _ = h.onboarding.Get(ctx, o.ID)
	if got.State != onboarding.StateOnHold || !strings.Contains(got.StateReason, "65510") || strings.Contains(got.StateReason, "655100000000013") {
		t.Fatalf("foreign SIM = %s %q, want ON_HOLD naming the network, not the IMSI", got.State, got.StateReason)
	}

	// The BSS account hears each move as a TMF688 event, without the IMSI.
	var events int
	var last string
	if err := db.QueryRowContext(ctx, `SELECT count(*), max(payload::text) FILTER (WHERE payload->>'state' = 'ON_HOLD') FROM tmf_events
		WHERE event_type = 'OnboardingStateChangeEvent' AND account_id = 'ACC-2'`).Scan(&events, &last); err != nil {
		t.Fatal(err)
	}
	if events != 2 || !strings.Contains(last, "65510") || strings.Contains(last, "655100000000013") || !strings.Contains(last, `"reasonCode": "PLMN_NOT_ALLOWED"`) {
		t.Fatalf("events = %d, on hold = %s", events, last)
	}
	var mismatch string
	if err := db.QueryRowContext(ctx, `SELECT payload->>'reasonCode' FROM tmf_events WHERE event_type = 'IdentityMismatch' AND account_id = 'ACC-2'`).Scan(&mismatch); err != nil || mismatch != "PLMN_NOT_ALLOWED" {
		t.Fatalf("IdentityMismatch = %q, %v", mismatch, err)
	}
	if got, _ = h.onboarding.Get(ctx, o.ID); got.StateCode != "PLMN_NOT_ALLOWED" {
		t.Fatalf("state_code = %q", got.StateCode)
	}
}

// TestIntegration_OnboardingNeverMovesAnOwnedDevice: a sale recorded for a
// serial that another customer's device already has holds; the device
// keeps its tenant.
func TestIntegration_OnboardingNeverMovesAnOwnedDevice(t *testing.T) {
	h, srv, ctx := newTestGateway(t)
	db := testDB(t, ctx)
	h.onboarding = onboarding.NewRepository(db)
	var custA, custB string
	if err := db.QueryRowContext(ctx, `INSERT INTO customers (id, name) VALUES (gen_random_uuid(), 'A') RETURNING id`).Scan(&custA); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO customers (id, name) VALUES (gen_random_uuid(), 'B') RETURNING id`).Scan(&custB); err != nil {
		t.Fatal(err)
	}
	cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
	cpe.post(vendorInform(t, "Nokia", "A4FC77", "3TG02241", "OB0004"))
	if _, err := db.ExecContext(ctx, `UPDATE devices SET customer_id = $1 WHERE serial_number = 'OB0004'`, custB); err != nil {
		t.Fatal(err)
	}
	// The sale also names an account that B owns: neither the account nor
	// the device may route its events to B.
	h.tmfEvents = bss.NewRepository(db)
	if _, err := db.ExecContext(ctx, `INSERT INTO bss_accounts (account_id, customer_id, claimed_by) VALUES ('B-ACC', $1, 'test')`, custB); err != nil {
		t.Fatal(err)
	}
	o, err := h.onboarding.Create(ctx, onboarding.Onboarding{ExpectedSerial: "OB0004", CustomerID: &custA, AccountRef: "B-ACC", Source: "console"})
	if err != nil {
		t.Fatal(err)
	}
	cpe = &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
	cpe.post(vendorInform(t, "Nokia", "A4FC77", "3TG02241", "OB0004"))
	got, _ := h.onboarding.Get(ctx, o.ID)
	if got.State != onboarding.StateOnHold || !strings.Contains(got.StateReason, "another customer") {
		t.Fatalf("state = %s %q, want ON_HOLD", got.State, got.StateReason)
	}
	var owner string
	if err := db.QueryRowContext(ctx, `SELECT customer_id::text FROM devices WHERE serial_number = 'OB0004'`).Scan(&owner); err != nil || owner != custB {
		t.Fatalf("device customer = %q, %v; want it unchanged", owner, err)
	}
	var toA, toB, attached int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE customer_id = $1::uuid), count(*) FILTER (WHERE customer_id = $2::uuid),
		count(*) FILTER (WHERE account_id IS NOT NULL OR device_id IS NOT NULL)
		FROM tmf_events WHERE payload->>'onboardingId' = $3`, custA, custB, o.ID).Scan(&toA, &toB, &attached); err != nil {
		t.Fatal(err)
	}
	if toA != 2 || toB != 0 || attached != 0 {
		t.Fatalf("sale events: to A %d, to B %d, with B's account or device %d; want 2, 0, 0", toA, toB, attached)
	}

	// The store refuses it too, whatever its caller decided.
	o2, err := h.onboarding.Create(ctx, onboarding.Onboarding{ExpectedSerial: "OB0004X", CustomerID: &custA, Source: "console"})
	if err != nil {
		t.Fatal(err)
	}
	var deviceID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM devices WHERE serial_number = 'OB0004'`).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	if moved, err := h.onboarding.Apply(ctx, *o2, onboarding.Decision{State: onboarding.StateContacted}, deviceID, nil); moved || !errors.Is(err, onboarding.ErrOtherTenant) {
		t.Fatalf("Apply across tenants = %v, %v; want ErrOtherTenant", moved, err)
	}
}

func simReadResponse(imsi string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:soap-enc="http://schemas.xmlsoap.org/soap/encoding/" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:cwmp="urn:dslforum-org:cwmp-1-0">
  <soap-env:Header><cwmp:ID soap-env:mustUnderstand="1">sim</cwmp:ID></soap-env:Header>
  <soap-env:Body><cwmp:GetParameterValuesResponse><ParameterList soap-enc:arrayType="cwmp:ParameterValueStruct[2]">
    <ParameterValueStruct><Name>Device.Cellular.Interface.1.USIM.ICCID</Name><Value xsi:type="xsd:string">89270000000000001234</Value></ParameterValueStruct>
    <ParameterValueStruct><Name>Device.Cellular.Interface.1.USIM.IMSI</Name><Value xsi:type="xsd:string">` + imsi + `</Value></ParameterValueStruct>
  </ParameterList></cwmp:GetParameterValuesResponse></soap-env:Body>
</soap-env:Envelope>`
}

// TestIntegration_OnboardingFinishesInOneSession proves the SIM read, the
// check, and the configuration it unlocks all happen in the session that
// first contacts the CPE, not one periodic Inform later.
func TestIntegration_OnboardingFinishesInOneSession(t *testing.T) {
	h, srv, ctx := newTestGateway(t)
	db := testDB(t, ctx)
	h.onboarding = onboarding.NewRepository(db)
	creds, err := credentials.NewRepository(db, "")
	if err != nil {
		t.Fatal(err)
	}
	h.credentials = creds
	tpl, err := h.templates.Create(ctx, "inform-interval", "", []templates.ParameterWrite{
		{Name: "Device.ManagementServer.PeriodicInformInterval", Value: "300", Type: "xsd:unsignedInt"}}, nil, false, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := h.onboarding.CreatePlan(ctx, onboarding.ServicePlan{Code: "P", Name: "P", AllowedPLMNs: []string{"65506"}, TemplateIDs: []string{tpl.ID}})
	if err != nil {
		t.Fatal(err)
	}
	o, err := h.onboarding.Create(ctx, onboarding.Onboarding{ExpectedSerial: "OB0003", ServicePlanID: &plan.ID, Source: "bss"})
	if err != nil {
		t.Fatal(err)
	}

	cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
	cpe.post(vendorInform(t, "Nokia", "A4FC77", "3TG02241", "OB0003"))
	got, _ := h.onboarding.Get(ctx, o.ID)
	cred, err := creds.Create(ctx, *got.DeviceID, credentials.TypeCWMPDigest, "ob3-user", "ob3-pass-0123456789", "ck")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Activate(ctx, cred.ID); err != nil {
		t.Fatal(err)
	}

	if _, body := cpe.post(""); !strings.Contains(body, "GetParameterValues") || !strings.Contains(body, "USIM.IMSI") {
		t.Fatalf("first RPC = %s, want the SIM read", body)
	}
	if _, body := cpe.post(simReadResponse("655060000000013")); !strings.Contains(body, "SetParameterValues") || !strings.Contains(body, "PeriodicInformInterval") {
		t.Fatalf("after the SIM read = %s, want the plan's template in the same session", body)
	}
	if got, _ = h.onboarding.Get(ctx, o.ID); got.State != onboarding.StateConfiguring {
		t.Fatalf("state = %s %q, want CONFIGURING", got.State, got.StateReason)
	}
}
