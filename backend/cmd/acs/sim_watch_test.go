package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"acs/internal/bss"
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/onboarding"
	"acs/internal/parameters"
	"acs/internal/simwatch"
	"acs/internal/simwatch/recorder"
	"acs/internal/store"
)

const (
	swICCID = "Device.Cellular.Interface.1.USIM.ICCID"
	swIMSI  = "Device.Cellular.Interface.1.USIM.IMSI"

	swICCIDA = "8927000000000000011"
	swICCIDB = "8927000000000000022"
	swIMSIA  = "655010000000011"
	swIMSIB  = "655010000000022"
	swIMSIX  = "999990000000033"
)

func newSIMWatchHandler(t *testing.T, interval time.Duration, withNames bool) (*handler, *devices.Device, context.Context, *sql.DB) {
	t.Helper()
	h, _, ctx := newTestGateway(t)
	db, err := store.Open(ctx, os.Getenv("ACS_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h.simWatch = &simWatchSampler{rec: recorder.New(db), interval: interval}
	h.onboarding = onboarding.NewRepository(db)
	dev, err := h.devices.UpsertFromInform(ctx, cwmp.DeviceID{Manufacturer: "Nokia", OUI: "A4FC77", ProductClass: "3TG02241", SerialNumber: "SIMWATCH1"}, []string{"0 BOOTSTRAP"})
	if err != nil {
		t.Fatal(err)
	}
	if withNames {
		caps := []parameters.SupportedCapability{{Path: swICCID}, {Path: swIMSI}, {Path: "Device.Cellular.Interface.2.USIM.IMSI"}}
		if err := h.params.SaveCapabilities(ctx, dev.ID, caps); err != nil {
			t.Fatal(err)
		}
	}
	return h, dev, ctx, db
}

func swJobs(t *testing.T, h *handler, ctx context.Context, id string) []jobs.Job {
	t.Helper()
	list, err := h.jobs.List(ctx, id, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var out []jobs.Job
	for _, j := range list {
		if j.CreatedBy == simwatch.CreatedBy {
			out = append(out, j)
		}
	}
	return out
}

type swRow struct{ state, network, iccid string }

func swState(t *testing.T, ctx context.Context, db *sql.DB, id string) (swRow, bool) {
	t.Helper()
	var r swRow
	err := db.QueryRowContext(ctx, `SELECT state, network, iccid_hint FROM device_sim_watch WHERE device_id=$1`, id).Scan(&r.state, &r.network, &r.iccid)
	if err == sql.ErrNoRows {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

func swReply(iccid, imsi string) []cwmp.ParameterValueStruct {
	var out []cwmp.ParameterValueStruct
	if iccid != "" {
		out = append(out, cwmp.ParameterValueStruct{Name: swICCID, Value: iccid})
	}
	if imsi != "" {
		out = append(out, cwmp.ParameterValueStruct{Name: swIMSI, Value: imsi})
	}
	return out
}

// putPlan makes the device IN_SERVICE under a plan that allows only 65501.
func putPlan(t *testing.T, h *handler, ctx context.Context, db *sql.DB, deviceID string) {
	t.Helper()
	plan, err := h.onboarding.CreatePlan(ctx, onboarding.ServicePlan{Code: "SW-PLAN", Name: "SW", AllowedPLMNs: []string{"65501"}, RequireDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	o, err := h.onboarding.Create(ctx, onboarding.Onboarding{ExpectedSerial: "SIMWATCH1", ServicePlanID: &plan.ID, Source: "console"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE onboardings SET device_id=$2, state='IN_SERVICE', in_service_at=now() WHERE id=$1`, o.ID, deviceID); err != nil {
		t.Fatal(err)
	}
}

func TestMaybeQueueSIMWatchQueuesOneIdentityReadWhenDue(t *testing.T) {
	h, dev, ctx, _ := newSIMWatchHandler(t, time.Hour, true)
	h.maybeQueueSIMWatch(ctx, dev)
	got := swJobs(t, h, ctx, dev.ID)
	if len(got) != 1 || got[0].Type != jobs.TypeGetParameter {
		t.Fatalf("jobs = %+v, want one GetParameterValues", got)
	}
	var p jobs.GetParameterPayload
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Paths) != 2 || p.Paths[0] != swICCID || p.Paths[1] != swIMSI {
		t.Fatalf("paths = %v, want only the interface 1 identity paths", p.Paths)
	}
	h.maybeQueueSIMWatch(ctx, dev)
	if n := len(swJobs(t, h, ctx, dev.ID)); n != 1 {
		t.Fatalf("jobs after repeat = %d, want 1", n)
	}
}

func TestMaybeQueueSIMWatchUsesTheStoredCheckTimeAcrossRestarts(t *testing.T) {
	h, dev, ctx, _ := newSIMWatchHandler(t, time.Hour, true)
	rec := h.simWatch.rec
	id := simwatch.Identity{ICCID: swICCIDA, IMSI: swIMSIA}
	if err := rec.Record(ctx, dev.ID, simwatch.Verdict{Kind: simwatch.KindBaseline, Network: "65501"}, id, simwatch.Identity{}, time.Now().UTC().Add(-time.Hour+2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// A fresh process has an empty throttle; the stored time makes it due.
	h.maybeQueueSIMWatch(ctx, dev)
	if n := len(swJobs(t, h, ctx, dev.ID)); n != 1 {
		t.Fatalf("just under one interval old: jobs = %d, want 1 (slack)", n)
	}
	h2, dev2, ctx2, _ := newSIMWatchHandler(t, time.Hour, true)
	if err := h2.simWatch.rec.Record(ctx2, dev2.ID, simwatch.Verdict{Kind: simwatch.KindBaseline}, id, simwatch.Identity{}, time.Now().UTC().Add(-time.Hour/2)); err != nil {
		t.Fatal(err)
	}
	h2.maybeQueueSIMWatch(ctx2, dev2)
	if n := len(swJobs(t, h2, ctx2, dev2.ID)); n != 0 {
		t.Fatalf("half an interval old: jobs = %d, want 0", n)
	}
}

func TestMaybeQueueSIMWatchSkips(t *testing.T) {
	cases := []struct {
		name     string
		interval time.Duration
		names    bool
		setup    func(t *testing.T, h *handler, ctx context.Context, dev *devices.Device)
	}{
		{name: "interval zero", interval: 0, names: true},
		{name: "open job by this creator", interval: time.Hour, names: true,
			setup: func(t *testing.T, h *handler, ctx context.Context, dev *devices.Device) {
				if _, err := h.jobs.Create(ctx, dev.ID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: []string{swIMSI}}, simwatch.CreatedBy); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "discovery active", interval: time.Hour, names: true,
			setup: func(t *testing.T, h *handler, ctx context.Context, dev *devices.Device) {
				if _, err := h.jobs.Create(ctx, dev.ID, jobs.TypeParameterDiscovery, jobs.ParameterDiscoveryPayload{}, "test"); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "names not discovered", interval: time.Hour, names: false},
		{name: "no SIM paths in the discovered names", interval: time.Hour, names: true,
			setup: func(t *testing.T, h *handler, ctx context.Context, dev *devices.Device) {
				if err := h.params.SaveCapabilities(ctx, dev.ID, []parameters.SupportedCapability{{Path: "Device.WiFi.SSID.1.SSID"}}); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, dev, ctx, _ := newSIMWatchHandler(t, tc.interval, tc.names)
			if tc.setup != nil {
				tc.setup(t, h, ctx, dev)
			}
			before := len(swJobs(t, h, ctx, dev.ID))
			h.maybeQueueSIMWatch(ctx, dev)
			if after := len(swJobs(t, h, ctx, dev.ID)); after != before {
				t.Fatalf("job created: %d -> %d", before, after)
			}
		})
	}
}

func TestSIMWatchIntervalFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
		warn bool
	}{
		{"", time.Hour, false}, {"0", 0, false}, {"30m", 30 * time.Minute, false},
		{"15", time.Hour, true}, {"-1m", time.Hour, true},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			t.Setenv("ACS_SIM_WATCH_INTERVAL", c.raw)
			var buf strings.Builder
			logger := slog.New(slog.NewTextHandler(io.Writer(&buf), nil))
			if got := simWatchIntervalFromEnv(logger); got != c.want {
				t.Fatalf("interval = %v, want %v", got, c.want)
			}
			if warned := strings.Contains(buf.String(), "ACS_SIM_WATCH_INTERVAL"); warned != c.warn {
				t.Fatalf("warned = %v, want %v (%q)", warned, c.warn, buf.String())
			}
		})
	}
}

func TestJudgeSIMBaselineThenNoticeThenAlert(t *testing.T) {
	h, dev, ctx, db := newSIMWatchHandler(t, time.Hour, true)
	putPlan(t, h, ctx, db, dev.ID)

	if c, skip := h.judgeSIM(ctx, dev.ID, swReply(swICCIDA, swIMSIA)); skip || c == nil || c.Verdict.Kind != simwatch.KindBaseline {
		t.Fatalf("first read: %+v skip=%v", c, skip)
	}
	if r, ok := swState(t, ctx, db, dev.ID); !ok || r.state != "ok" {
		t.Fatalf("after baseline: %+v %v", r, ok)
	}
	// The cache holds the SIM the device reported, as cacheParameterValues would leave it.
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIA), "GET_PARAMETER_VALUES")

	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDB, swIMSIB), "GET_PARAMETER_VALUES")
	if r, _ := swState(t, ctx, db, dev.ID); r.state != "notice" || r.network != "65501" {
		t.Fatalf("new SIM on an allowed network: %+v", r)
	}
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIX), "GET_PARAMETER_VALUES")
	if r, _ := swState(t, ctx, db, dev.ID); r.state != "alert" || r.network != "99999" {
		t.Fatalf("new SIM on a network the plan does not allow: %+v", r)
	}
}

func TestJudgeSIMWithNoPlanAllowsEveryNetwork(t *testing.T) {
	h, dev, ctx, db := newSIMWatchHandler(t, time.Hour, true)
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIA), "GET_PARAMETER_VALUES")
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDB, swIMSIX), "GET_PARAMETER_VALUES")
	if r, _ := swState(t, ctx, db, dev.ID); r.state != "notice" {
		t.Fatalf("no plan must give a notice, got %+v", r)
	}
}

func TestJudgeSIMOnlyNewIMSIFiresOnceAndWorksWithoutEvents(t *testing.T) {
	h, dev, ctx, db := newSIMWatchHandler(t, time.Hour, true)
	h.tmfEvents = nil
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIA), "GET_PARAMETER_VALUES")
	h.cacheParameterValues(ctx, dev.ID, swReply("", swIMSIB), "GET_PARAMETER_VALUES")
	r, ok := swState(t, ctx, db, dev.ID)
	if !ok || r.state != "notice" {
		t.Fatalf("only the IMSI changed, events off: %+v %v", r, ok)
	}
	var detected time.Time
	if err := db.QueryRowContext(ctx, `SELECT detected_at FROM device_sim_watch WHERE device_id=$1`, dev.ID).Scan(&detected); err != nil {
		t.Fatal(err)
	}
	h.cacheParameterValues(ctx, dev.ID, swReply("", swIMSIB), "GET_PARAMETER_VALUES")
	var again time.Time
	if err := db.QueryRowContext(ctx, `SELECT detected_at FROM device_sim_watch WHERE device_id=$1`, dev.ID).Scan(&again); err != nil || !again.Equal(detected) {
		t.Fatalf("the same SIM seen again changed detected_at: %v -> %v (%v)", detected, again, err)
	}
}

func TestJudgeSIMIsNotRoutedByCreator(t *testing.T) {
	// An operator's probe reply goes through cacheParameterValues with its own
	// source and is judged exactly like the sampler's.
	h, dev, ctx, db := newSIMWatchHandler(t, time.Hour, true)
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIA), "OPERATOR_PROBE")
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDB, swIMSIA), "OPERATOR_PROBE")
	if r, _ := swState(t, ctx, db, dev.ID); r.state != "notice" {
		t.Fatalf("state = %+v", r)
	}
}

func TestJudgeSIMIgnoresRepliesWithoutIdentity(t *testing.T) {
	h, dev, ctx, db := newSIMWatchHandler(t, time.Hour, true)
	h.cacheParameterValues(ctx, dev.ID, []cwmp.ParameterValueStruct{{Name: "Device.Cellular.Interface.2.USIM.IMSI", Value: swIMSIX}}, "GET_PARAMETER_VALUES")
	if _, ok := swState(t, ctx, db, dev.ID); ok {
		t.Fatal("interface 2 must not create a verdict")
	}
}

func TestJudgeSIMKeepsTheOldSIMInTheCacheWhenItCannotBePersisted(t *testing.T) {
	h, dev, ctx, db := newSIMWatchHandler(t, time.Hour, true)
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIA), "GET_PARAMETER_VALUES")
	if _, err := db.ExecContext(ctx, `ALTER TABLE device_sim_watch RENAME TO device_sim_watch_off`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `ALTER TABLE IF EXISTS device_sim_watch_off RENAME TO device_sim_watch`)
	})
	other := "Device.Cellular.Interface.1.USIM.Status"
	h.cacheParameterValues(ctx, dev.ID, append(swReply(swICCIDB, swIMSIB), cwmp.ParameterValueStruct{Name: other, Value: "Available"}), "GET_PARAMETER_VALUES")
	cache, err := h.params.Get(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cache[swICCID].Value != swICCIDA || cache[swIMSI].Value != swIMSIA {
		t.Fatalf("identity overwritten although the verdict could not be saved: %q %q", cache[swICCID].Value, cache[swIMSI].Value)
	}
	if cache[other].Value != "Available" {
		t.Fatal("other values must still be cached")
	}
}

// simEventSink records the events the ACS publishes.
type simEventSink struct {
	types    []string
	payloads []string
}

func (s *simEventSink) CreateEvent(_ context.Context, _, _, _, _, eventType string, payload json.RawMessage, _ time.Time) (*bss.EventRecord, error) {
	s.types = append(s.types, eventType)
	s.payloads = append(s.payloads, string(payload))
	return nil, nil
}
func (s *simEventSink) CreateAlarm(context.Context, string, string, string, string, string, string, string, string, json.RawMessage) (*bss.AlarmRecord, error) {
	return nil, nil
}
func (s *simEventSink) ClearAlarm(context.Context, string, string, string) error { return nil }

func simEvents(s *simEventSink) (n int, payload string) {
	for i, ty := range s.types {
		if ty == "SimChanged" {
			n++
			payload = s.payloads[i]
		}
	}
	return n, payload
}

func TestSimChangedIsPublishedOnlyForNoticeAndAlert(t *testing.T) {
	h, dev, ctx, db := newSIMWatchHandler(t, time.Hour, true)
	sink := &simEventSink{}
	h.tmfEvents = sink
	putPlan(t, h, ctx, db, dev.ID)
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIA), "GET_PARAMETER_VALUES") // baseline
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIA), "GET_PARAMETER_VALUES") // unchanged
	if n, _ := simEvents(sink); n != 0 {
		t.Fatalf("baseline and unchanged published %d SimChanged events", n)
	}
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDB, swIMSIX), "GET_PARAMETER_VALUES")
	n, payload := simEvents(sink)
	if n != 1 || !strings.Contains(payload, `"verdict":"alert"`) || !strings.Contains(payload, `"network":"99999"`) {
		t.Fatalf("events = %d, payload = %s", n, payload)
	}
	for _, full := range []string{swICCIDA, swICCIDB, swIMSIA, swIMSIX} {
		if strings.Contains(payload, full) {
			t.Fatalf("event payload carries a full identifier: %s", payload)
		}
	}
}

// A SIM pulled out reads as empty or a sentinel. That blank must not replace
// the known SIM in the cache, or the next SIM would look like a first one.
func TestBlankIdentityReadDoesNotResetTheBaseline(t *testing.T) {
	h, dev, ctx, db := newSIMWatchHandler(t, time.Hour, true)
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIA), "GET_PARAMETER_VALUES")
	h.cacheParameterValues(ctx, dev.ID, []cwmp.ParameterValueStruct{{Name: swICCID, Value: ""}, {Name: swIMSI, Value: "-32768"}}, "GET_PARAMETER_VALUES")
	cache, err := h.params.Get(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cache[swICCID].Value != swICCIDA || cache[swIMSI].Value != swIMSIA {
		t.Fatalf("a blank reading replaced the known SIM: %q %q", cache[swICCID].Value, cache[swIMSI].Value)
	}
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDB, swIMSIB), "GET_PARAMETER_VALUES")
	if r, _ := swState(t, ctx, db, dev.ID); r.state != "notice" {
		t.Fatalf("a new SIM after a blank reading must be a notice, got %+v", r)
	}
}

// A reply that carries only the new IMSI must not leave the old ICCID in the
// cache: the next full read would be judged against it and fire the same swap
// a second time, wiping an approval made in between.
func TestAPartialReplyDoesNotFireTheSameSwapTwice(t *testing.T) {
	h, dev, ctx, db := newSIMWatchHandler(t, time.Hour, true)
	sink := &simEventSink{}
	h.tmfEvents = sink
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIA), "GET_PARAMETER_VALUES")
	h.cacheParameterValues(ctx, dev.ID, swReply("", swIMSIB), "GET_PARAMETER_VALUES") // IMSI only
	if r, _ := swState(t, ctx, db, dev.ID); r.state != "notice" {
		t.Fatalf("after the partial reply: %+v", r)
	}
	if _, err := db.ExecContext(ctx, `UPDATE device_sim_watch SET state='ok', approved_at=now(), approved_by='alice', approval_reason='Customer swapped the SIM on site' WHERE device_id=$1`, dev.ID); err != nil {
		t.Fatal(err)
	}
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDB, swIMSIB), "GET_PARAMETER_VALUES") // the full read
	if r, _ := swState(t, ctx, db, dev.ID); r.state != "ok" {
		t.Fatalf("the approved swap fired again: %+v", r)
	}
	if n, _ := simEvents(sink); n != 1 {
		t.Fatalf("SimChanged events = %d, want 1", n)
	}
}

// A CPE that flips its identity values reply after reply must not send a
// SimChanged webhook for every flip: one per gap, though an upgrade to an
// alert still goes out at once.
func TestSimChangedIsCoalescedPerDevice(t *testing.T) {
	h, dev, ctx, db := newSIMWatchHandler(t, time.Hour, true)
	sink := &simEventSink{}
	h.tmfEvents = sink
	putPlan(t, h, ctx, db, dev.ID)
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIA), "GET_PARAMETER_VALUES")
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDB, swIMSIB), "GET_PARAMETER_VALUES") // notice
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDA, swIMSIA), "GET_PARAMETER_VALUES") // flips back: notice again
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDB, swIMSIB), "GET_PARAMETER_VALUES") // and again
	if n, _ := simEvents(sink); n != 1 {
		t.Fatalf("SimChanged events after three flips = %d, want 1", n)
	}
	h.cacheParameterValues(ctx, dev.ID, swReply(swICCIDB, swIMSIX), "GET_PARAMETER_VALUES") // alert
	if n, _ := simEvents(sink); n != 2 {
		t.Fatalf("an upgrade to an alert must be announced at once, events = %d, want 2", n)
	}
}
