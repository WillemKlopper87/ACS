package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"acs/internal/cpemetrics"
	"acs/internal/cpemetrics/recorder"
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"acs/internal/store"
)

const cpmPrefix = "Device.Cellular.Interface.1."

func cpmNames() []parameters.SupportedCapability {
	var caps []parameters.SupportedCapability
	for _, leaf := range []string{
		"RSSI",
		"X_ALU-COM_Attached5GCellStat.RSRPCurrent",
		"X_ALU-COM_Attached5GCellStat.RSRQCurrent",
		"X_ALU-COM_Attached5GCellStat.SNRCurrent",
	} {
		caps = append(caps, parameters.SupportedCapability{Path: cpmPrefix + leaf})
	}
	return caps
}

// newCPEMetricsHandler builds the standard DB-backed test handler with the
// sampler wired and one DEVICE2 device whose names include signal paths.
func newCPEMetricsHandler(t *testing.T, interval time.Duration, withNames bool) (*handler, *devices.Device, context.Context) {
	t.Helper()
	h, _, ctx := newTestGateway(t)
	db, err := store.Open(ctx, os.Getenv("ACS_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h.cpeMetrics = &cpeMetricsSampler{repo: cpemetrics.NewRepository(db), rec: recorder.New(db), interval: interval}
	dev, err := h.devices.UpsertFromInform(ctx, cwmp.DeviceID{Manufacturer: "Nokia", OUI: "A4FC77", ProductClass: "3TG02241", SerialNumber: "CPEMETRICS1"}, []string{"0 BOOTSTRAP"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.devices.UpdateDataModelRoot(ctx, dev.ID, devices.DataModelRootDevice2); err != nil {
		t.Fatal(err)
	}
	dev.DataModelRoot = devices.DataModelRootDevice2
	if withNames {
		if err := h.params.SaveCapabilities(ctx, dev.ID, cpmNames()); err != nil {
			t.Fatal(err)
		}
	}
	return h, dev, ctx
}

func cpmJobs(t *testing.T, h *handler, ctx context.Context, id string) []jobs.Job {
	t.Helper()
	list, err := h.jobs.List(ctx, id, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var out []jobs.Job
	for _, j := range list {
		if j.CreatedBy == cpemetrics.CreatedBy {
			out = append(out, j)
		}
	}
	return out
}

// cpmCount reads acs_cpe_samples_total{result} through the registry, so no
// test-only dependency is needed.
func cpmCount(t *testing.T, h *handler, result string) float64 {
	t.Helper()
	fams, err := h.metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != "acs_cpe_samples_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "result" && l.GetValue() == result {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func cpmFloat(f float64) *float64 { return &f }

func TestMaybeQueueCPEMetricsQueuesOneReadWhenDue(t *testing.T) {
	h, dev, ctx := newCPEMetricsHandler(t, 15*time.Minute, true)
	h.maybeQueueCPEMetrics(ctx, dev)
	got := cpmJobs(t, h, ctx, dev.ID)
	if len(got) != 1 {
		t.Fatalf("jobs = %d, want 1", len(got))
	}
	if got[0].Type != jobs.TypeGetParameter {
		t.Fatalf("type = %s", got[0].Type)
	}
	var p jobs.GetParameterPayload
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	want := cpemetrics.PlanReads(devices.DataModelRootDevice2, map[string]bool{
		cpmPrefix + "RSSI": false,
		cpmPrefix + "X_ALU-COM_Attached5GCellStat.RSRPCurrent": false,
		cpmPrefix + "X_ALU-COM_Attached5GCellStat.RSRQCurrent": false,
		cpmPrefix + "X_ALU-COM_Attached5GCellStat.SNRCurrent":  false,
	})
	if len(want) == 0 || len(p.Paths) != len(want) {
		t.Fatalf("paths = %v, want %d planned", p.Paths, len(want))
	}
	for i, r := range want {
		if p.Paths[i] != r.Path {
			t.Fatalf("path %d = %s, want %s", i, p.Paths[i], r.Path)
		}
	}
	// Throttled: an immediate second check-in queues nothing more.
	h.maybeQueueCPEMetrics(ctx, dev)
	if n := len(cpmJobs(t, h, ctx, dev.ID)); n != 1 {
		t.Fatalf("jobs after repeat = %d, want 1", n)
	}
}

func TestMaybeQueueCPEMetricsSkips(t *testing.T) {
	cases := []struct {
		name     string
		interval time.Duration
		names    bool
		setup    func(t *testing.T, h *handler, ctx context.Context, dev *devices.Device)
	}{
		{name: "interval zero", interval: 0, names: true},
		{name: "last sample newer than interval", interval: 15 * time.Minute, names: true,
			setup: func(t *testing.T, h *handler, ctx context.Context, dev *devices.Device) {
				if err := h.cpeMetrics.rec.Record(ctx, dev.ID, time.Now().UTC().Add(-time.Minute), cpemetrics.Sample{RSRP: cpmFloat(-90)}); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "open job by this creator", interval: 15 * time.Minute, names: true,
			setup: func(t *testing.T, h *handler, ctx context.Context, dev *devices.Device) {
				if _, err := h.jobs.Create(ctx, dev.ID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: []string{"Device.X"}}, cpemetrics.CreatedBy); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "discovery active", interval: 15 * time.Minute, names: true,
			setup: func(t *testing.T, h *handler, ctx context.Context, dev *devices.Device) {
				if _, err := h.jobs.Create(ctx, dev.ID, jobs.TypeParameterDiscovery, jobs.ParameterDiscoveryPayload{}, "test"); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "names not discovered", interval: 15 * time.Minute, names: false},
		{name: "non-cellular device", interval: 15 * time.Minute, names: true,
			setup: func(t *testing.T, h *handler, ctx context.Context, dev *devices.Device) {
				if err := h.params.SaveCapabilities(ctx, dev.ID, []parameters.SupportedCapability{{Path: "Device.WiFi.SSID.1.SSID"}}); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, dev, ctx := newCPEMetricsHandler(t, tc.interval, tc.names)
			if tc.setup != nil {
				tc.setup(t, h, ctx, dev)
			}
			before := len(cpmJobs(t, h, ctx, dev.ID))
			h.maybeQueueCPEMetrics(ctx, dev)
			if after := len(cpmJobs(t, h, ctx, dev.ID)); after != before {
				t.Fatalf("job created: %d -> %d", before, after)
			}
		})
	}
}

func TestRecordCPEMetricsStoresAndIgnoresEmpty(t *testing.T) {
	h, dev, ctx := newCPEMetricsHandler(t, 15*time.Minute, true)
	r := cpmPrefix + "X_ALU-COM_Attached5GCellStat."
	h.recordCPEMetrics(ctx, dev.ID, []cwmp.ParameterValueStruct{
		{Name: r + "RSRPCurrent", Value: "-95"},
		{Name: r + "RSRQCurrent", Value: "-11"},
		{Name: r + "SNRCurrent", Value: "7"},
	})
	hist, err := h.cpeMetrics.repo.History(ctx, dev.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist.Samples) != 1 || hist.Samples[0].RSRP == nil || *hist.Samples[0].RSRP != -95 {
		t.Fatalf("history = %+v", hist.Samples)
	}
	if v := cpmCount(t, h, "recorded"); v != 1 {
		t.Fatalf("recorded = %v", v)
	}

	h.recordCPEMetrics(ctx, dev.ID, []cwmp.ParameterValueStruct{
		{Name: r + "RSRPCurrent", Value: "-32768"},
		{Name: r + "RSRQCurrent", Value: "-32768"},
	})
	hist, _ = h.cpeMetrics.repo.History(ctx, dev.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Minute), 100)
	if len(hist.Samples) != 1 {
		t.Fatalf("sentinel reply wrote a row: %d", len(hist.Samples))
	}
	if v := cpmCount(t, h, "empty"); v != 1 {
		t.Fatalf("empty = %v", v)
	}
}

func TestCPEMetricsReplyBypassesTheParameterCache(t *testing.T) {
	h, dev, ctx := newCPEMetricsHandler(t, 15*time.Minute, true)
	db, err := store.Open(ctx, os.Getenv("ACS_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := cpmPrefix + "X_ALU-COM_Attached5GCellStat."
	h.recordCPEMetrics(ctx, dev.ID, []cwmp.ParameterValueStruct{{Name: r + "RSRPCurrent", Value: "-95"}})
	cache, err := h.params.Get(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cache[r+"RSRPCurrent"]; ok {
		t.Fatalf("parameter cache changed: %v", cache)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM parameter_history WHERE device_id=$1`, dev.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("parameter_history rows = %d", n)
	}
}
