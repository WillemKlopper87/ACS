package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"acs/internal/scheduler"
)

// TestSchedulableJobTypesMatchWorker keeps scheduler.SchedulableJobTypes
// — what createScheduledJob validates against — in step with the switch
// in schedule_worker.go that actually dispatches them.
//
// Drift in either direction is a real bug, which is why this is asserted
// rather than left to review:
//
//   - a type in the set but not the switch means the API accepts a
//     schedule that reads as enabled in the console, never fires, and
//     logs an error on every worker tick for as long as it exists;
//   - a type in the switch but not the set means a job the worker is
//     perfectly capable of running is rejected at creation with a 400.
//
// Read as text, the same way the OpenAPI drift test reads routes.go: the
// switch is a language construct, not something the package exports.
func TestSchedulableJobTypesMatchWorker(t *testing.T) {
	src, err := os.ReadFile("schedule_worker.go")
	if err != nil {
		t.Fatal(err)
	}

	// The dispatch switch is the one over sj.JobType; take the cases
	// between it and its default.
	start := strings.Index(string(src), "switch sj.JobType {")
	if start < 0 {
		t.Fatal("could not find the `switch sj.JobType` dispatch in schedule_worker.go — this test's extraction is stale")
	}
	body := string(src)[start:]
	if end := strings.Index(body, "\n\tdefault:"); end > 0 {
		body = body[:end]
	}

	// `case jobs.TypeSetParameter:` → SET_PARAMETER, resolved via the
	// jobs package constants rather than assumed from the identifier.
	constToValue := map[string]string{
		"TypeSetParameter":          "SET_PARAMETER",
		"TypeGetParameter":          "GET_PARAMETER",
		"TypeDiagnosticsPing":       "DIAGNOSTICS_PING",
		"TypeConnectionRequest":     "CONNECTION_REQUEST",
		"TypeDiagnosticsTraceroute": "DIAGNOSTICS_TRACEROUTE",
		"TypeReboot":                "REBOOT",
		"TypeFactoryReset":          "FACTORY_RESET",
		"TypeFirmwareDownload":      "FIRMWARE_DOWNLOAD",
	}

	var dispatched []string
	for _, m := range regexp.MustCompile(`case jobs\.(\w+):`).FindAllStringSubmatch(body, -1) {
		value, ok := constToValue[m[1]]
		if !ok {
			t.Fatalf("worker dispatches jobs.%s, which this test doesn't know the wire value of — add it to constToValue", m[1])
		}
		dispatched = append(dispatched, value)
	}
	if len(dispatched) == 0 {
		t.Fatal("extracted no cases from the dispatch switch — this test's extraction is stale")
	}
	sort.Strings(dispatched)

	declared := scheduler.SchedulableJobTypeList()
	if strings.Join(dispatched, ",") != strings.Join(declared, ",") {
		t.Errorf("scheduler.SchedulableJobTypes and schedule_worker.go's dispatch switch disagree:\n"+
			"  declared schedulable: %s\n  worker dispatches:    %s",
			strings.Join(declared, ", "), strings.Join(dispatched, ", "))
	}
}

// A schedule runs unattended and on repeat, so the destructive one-shot
// job types must stay off the schedulable list even if someone adds a
// worker case for them.
func TestDestructiveJobTypesAreNotSchedulable(t *testing.T) {
	for _, jobType := range []string{"FACTORY_RESET", "REBOOT", "FIRMWARE_DOWNLOAD"} {
		if scheduler.SchedulableJobTypes[jobType] {
			t.Errorf("%s must not be schedulable: a schedule fires unattended and repeatedly", jobType)
		}
	}
}

// TestScheduledJobResponseRedactsSecrets: a schedule's payload is stored
// with the values its jobs will write, but GET /schedules must never
// return a Wi-Fi passphrase or credential (handoff plan 2026-10-30 §3a.5).
func TestScheduledJobResponseRedactsSecrets(t *testing.T) {
	sj := &scheduler.ScheduledJob{
		ID: "s1", JobType: "SET_PARAMETER",
		Payload: []byte(`{"parameters":[{"name":"Device.WiFi.AccessPoint.1.Security.KeyPassphrase","value":"hunter2hunter2"},{"name":"Device.WiFi.SSID.1.SSID","value":"SENT-5G"}]}`),
	}
	got := string(toScheduledJobResponse(sj).Payload)
	if strings.Contains(got, "hunter2hunter2") || !strings.Contains(got, "SENT-5G") {
		t.Fatalf("payload = %s, want passphrase redacted and SSID kept", got)
	}
	if !strings.Contains(string(sj.Payload), "hunter2hunter2") {
		t.Fatal("stored payload modified: the worker still needs the value")
	}
}
