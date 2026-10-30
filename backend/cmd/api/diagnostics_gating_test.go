package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"acs/internal/devices"
	"acs/internal/diagnostics"
	"acs/internal/jobs"
	"acs/internal/operators"
)

const testPingPrefix = "Device.IP.Diagnostics.IPPing."

// pingNames is the IPPing subtree as the FastMile's discovery reports it.
func pingNames() map[string]bool {
	names := map[string]bool{testPingPrefix: false}
	for _, leaf := range []string{"Host", "NumberOfRepetitions", "Timeout", "DataBlockSize", "DSCP", "DiagnosticsState"} {
		names[testPingPrefix+leaf] = true
	}
	for _, leaf := range diagnostics.PingResultLeaves {
		names[testPingPrefix+leaf] = false
	}
	return names
}

// TestScheduledPingPayloadAppliesInteractiveDefaults: the console stores
// only {host} for a scheduled ping, which used to go out with
// NumberOfRepetitions/Timeout/DataBlockSize = 0.
func TestScheduledPingPayloadAppliesInteractiveDefaults(t *testing.T) {
	device := &devices.Device{DataModelRoot: devices.DataModelRootDevice2}
	p, err := scheduledPingPayload(jobs.DiagnosticsPingPayload{Host: "8.8.8.8"}, device, pingNames())
	if err != nil {
		t.Fatal(err)
	}
	if p.NumberOfRepetitions != diagnostics.PingDefaultRepetitions || p.Timeout != diagnostics.PingDefaultTimeoutMS || p.DataBlockSize != diagnostics.PingDefaultBlockSize {
		t.Fatalf("defaults not applied: %+v", p)
	}
	if p.Prefix != testPingPrefix || len(p.Trigger) != 6 || len(p.PollPaths) == 0 {
		t.Fatalf("plan not resolved: %+v", p)
	}
	for _, w := range p.Trigger {
		if strings.HasSuffix(w.Name, "NumberOfRepetitions") && (w.Value != "4" || w.Type != "xsd:unsignedInt") {
			t.Errorf("NumberOfRepetitions write = %+v", w)
		}
	}

	// A group member without diagnostics (Nokia Beacon 3.1) is skipped.
	_, err = scheduledPingPayload(jobs.DiagnosticsPingPayload{Host: "8.8.8.8"}, device, map[string]bool{"Device.WiFi.": false})
	var unsupported *diagnostics.UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want UnsupportedError", err)
	}
}

func TestCreatePingIsGatedOnDiscovery(t *testing.T) {
	e := newTestEnv(t)
	e.operator("alice", operators.RoleSuperAdmin)

	undiscovered := e.device("DIAG-UNDISC", nil)
	if r := e.call("alice", http.MethodPost, "/api/v1/devices/"+undiscovered+"/diagnostics/ping", map[string]string{"host": "8.8.8.8"}); r.code != http.StatusConflict || !strings.Contains(r.body, "discover") {
		t.Fatalf("undiscovered device -> %d %s, want 409 asking for discovery", r.code, r.body)
	}

	beacon := e.device("DIAG-BEACON", nil)
	if err := e.h.params.SaveNames(e.ctx, beacon, map[string]bool{"Device.WiFi.": false, "Device.WiFi.SSID.1.SSID": true}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"ping", "traceroute"} {
		if r := e.call("alice", http.MethodPost, "/api/v1/devices/"+beacon+"/diagnostics/"+path, map[string]string{"host": "8.8.8.8"}); r.code != http.StatusConflict {
			t.Fatalf("%s on a device without Device.IP.Diagnostics -> %d %s, want 409", path, r.code, r.body)
		}
	}
	if list, _ := e.h.jobs.List(e.ctx, beacon, nil, false); len(list) != 0 {
		t.Fatalf("rejected requests queued %d job(s)", len(list))
	}

	fastMile := e.device("DIAG-FASTMILE", nil)
	if err := e.h.params.SaveNames(e.ctx, fastMile, pingNames()); err != nil {
		t.Fatal(err)
	}
	if r := e.call("alice", http.MethodPost, "/api/v1/devices/"+fastMile+"/diagnostics/ping", map[string]string{"host": "8.8.8.8"}); r.code != http.StatusAccepted {
		t.Fatalf("ping on a discovered IPPing -> %d %s, want 202", r.code, r.body)
	}
	list, err := e.h.jobs.List(e.ctx, fastMile, nil, false)
	if err != nil || len(list) != 1 {
		t.Fatalf("jobs = %+v %v", list, err)
	}
	var payload jobs.DiagnosticsPingPayload
	if err := json.Unmarshal(list[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if list[0].MaxAttempts != jobs.DiagnosticsMaxAttempts || len(payload.Trigger) != 6 || payload.Trigger[5].Value != "Requested" {
		t.Fatalf("job max_attempts %d payload %+v", list[0].MaxAttempts, payload)
	}

	// Traceroute where Timeout/DSCP are absent (FastMile): queued, inputs skipped.
	const tr = "Device.IP.Diagnostics.TraceRoute."
	names := pingNames()
	for _, leaf := range []string{"Host", "NumberOfTries", "DataBlockSize", "MaxHopCount", "DiagnosticsState"} {
		names[tr+leaf] = true
	}
	names[tr+"RouteHopsNumberOfEntries"] = false
	if err := e.h.params.SaveNames(e.ctx, fastMile, names); err != nil {
		t.Fatal(err)
	}
	r := e.call("alice", http.MethodPost, "/api/v1/devices/"+fastMile+"/diagnostics/traceroute", map[string]string{"host": "8.8.8.8"})
	if r.code != http.StatusAccepted || !strings.Contains(r.body, `"skipped_inputs":["Timeout","DSCP"]`) {
		t.Fatalf("traceroute -> %d %s, want 202 listing skipped Timeout/DSCP", r.code, r.body)
	}

	caps := e.call("alice", http.MethodGet, "/api/v1/devices/"+beacon+"/diagnostics-capabilities", nil)
	if caps.code != http.StatusOK || !strings.Contains(caps.body, `"ping":{"supported":false`) {
		t.Fatalf("beacon capabilities -> %d %s", caps.code, caps.body)
	}
}
