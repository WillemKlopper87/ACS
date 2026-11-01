package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"acs/internal/devices"
	"acs/internal/jobs"
)

const pingPrefix = "Device.IP.Diagnostics.IPPing."

func pingJob(t *testing.T, payload jobs.DiagnosticsPingPayload, attempts int, deadline *time.Time) *jobs.Job {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return &jobs.Job{Type: jobs.TypeDiagnosticsPing, Payload: raw, Attempts: attempts, MaxAttempts: jobs.DiagnosticsMaxAttempts,
		CommandKey: "diag_test", DiagnosticsDeadline: deadline}
}

// C1: whether a dispatch triggers or polls depends on the recorded trigger
// acknowledgement, not on the attempt counter — a trigger requeued after a
// dropped session must be sent again, not polled.
func TestDiagnosticRenderTriggerOrPollByAcknowledgement(t *testing.T) {
	h := &handler{}
	payload := jobs.DiagnosticsPingPayload{Host: "8.8.8.8", NumberOfRepetitions: 4, Timeout: 5000, DataBlockSize: 64, Prefix: pingPrefix}

	body, ok := h.renderJobRequest(pingJob(t, payload, 3, nil))
	if !ok || !strings.Contains(string(body), "<cwmp:SetParameterValues>") {
		t.Fatalf("unacknowledged trigger at attempt 3 rendered %s, want SetParameterValues", body)
	}
	deadline := time.Now().Add(time.Minute)
	body, ok = h.renderJobRequest(pingJob(t, payload, 1, &deadline))
	if !ok || !strings.Contains(string(body), "<cwmp:GetParameterValues>") {
		t.Fatalf("acknowledged trigger rendered %s, want GetParameterValues", body)
	}
}

// C8: numeric inputs of a job queued without a discovery plan still go out
// typed, with DiagnosticsState=Requested written last.
func TestLegacyDiagnosticTriggerIsTyped(t *testing.T) {
	h := &handler{}
	body, _ := h.renderJobRequest(pingJob(t, jobs.DiagnosticsPingPayload{Host: "8.8.8.8", NumberOfRepetitions: 4, Timeout: 5000, DataBlockSize: 64, DSCP: 0, Prefix: pingPrefix}, 1, nil))
	s := string(body)
	for _, want := range []string{
		`<Name>` + pingPrefix + `Host</Name><Value xsi:type="xsd:string">8.8.8.8</Value>`,
		`<Name>` + pingPrefix + `NumberOfRepetitions</Name><Value xsi:type="xsd:unsignedInt">4</Value>`,
		`<Name>` + pingPrefix + `Timeout</Name><Value xsi:type="xsd:unsignedInt">5000</Value>`,
		`<Name>` + pingPrefix + `DataBlockSize</Name><Value xsi:type="xsd:unsignedInt">64</Value>`,
		`<Name>` + pingPrefix + `DSCP</Name><Value xsi:type="xsd:unsignedInt">0</Value>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("trigger missing %s\n%s", want, s)
		}
	}
	if strings.LastIndex(s, "DiagnosticsState") < strings.LastIndex(s, "DSCP") {
		t.Error("DiagnosticsState must be written after every input")
	}

	raw, _ := json.Marshal(jobs.DiagnosticsTraceroutePayload{Host: "8.8.8.8", NumberOfTries: 3, Timeout: 5000, DataBlockSize: 38, MaxHopCount: 30, Prefix: "Device.IP.Diagnostics.TraceRoute."})
	body, _ = h.renderJobRequest(&jobs.Job{Type: jobs.TypeDiagnosticsTraceroute, Payload: raw, Attempts: 1})
	if !strings.Contains(string(body), `MaxHopCount</Name><Value xsi:type="xsd:unsignedInt">30</Value>`) {
		t.Errorf("traceroute MaxHopCount untyped: %s", body)
	}
}

// A discovery plan stored on the job is sent verbatim: only discovered
// writable inputs, and only discovered result leaves polled.
func TestPlannedDiagnosticTriggerAndPollAreUsed(t *testing.T) {
	h := &handler{}
	payload := jobs.DiagnosticsTraceroutePayload{Host: "8.8.8.8", NumberOfTries: 3, Timeout: 5000, DSCP: 0, MaxHopCount: 30, Prefix: "Device.IP.Diagnostics.TraceRoute."}
	payload.Trigger = []jobs.ParameterWrite{
		{Name: payload.Prefix + "Host", Value: "8.8.8.8", Type: "xsd:string"},
		{Name: payload.Prefix + "MaxHopCount", Value: "30", Type: "xsd:unsignedInt"},
		{Name: payload.Prefix + "DiagnosticsState", Value: "Requested", Type: "xsd:string"},
	}
	payload.PollPaths = []string{payload.Prefix + "DiagnosticsState", payload.Prefix + "RouteHopsNumberOfEntries"}
	raw, _ := json.Marshal(payload)

	body, _ := h.renderJobRequest(&jobs.Job{Type: jobs.TypeDiagnosticsTraceroute, Payload: raw, Attempts: 1})
	if s := string(body); strings.Contains(s, "Timeout") || strings.Contains(s, "DSCP") || !strings.Contains(s, "ParameterValueStruct[3]") {
		t.Fatalf("planned trigger not used verbatim: %s", s)
	}
	deadline := time.Now().Add(time.Minute)
	body, _ = h.renderJobRequest(&jobs.Job{Type: jobs.TypeDiagnosticsTraceroute, Payload: raw, Attempts: 2, DiagnosticsDeadline: &deadline})
	if s := string(body); strings.Contains(s, "ResponseTime</string>") || !strings.Contains(s, "xsd:string[2]") {
		t.Fatalf("planned poll not used verbatim: %s", s)
	}
}

func TestClassifyDiagnosticsState(t *testing.T) {
	for state, want := range map[string]diagStateClass{
		"": diagPending, "Requested": diagPending, "None": diagPending,
		"Complete": diagComplete, "Completed": diagComplete,
		"Error_CannotResolveHostName": diagFailed, "Error_Internal": diagFailed, "Canceled": diagFailed,
	} {
		if got := classifyDiagnosticsState(state); got != want {
			t.Errorf("classifyDiagnosticsState(%q) = %d, want %d", state, got, want)
		}
	}
}

func diagnosticsCompleteInform(t *testing.T) string {
	return strings.Replace(fixture(t, "inform_bootstrap.xml"), "0 BOOTSTRAP", "8 DIAGNOSTICS COMPLETE", 1)
}

func pingPollResponse(state string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:soap-enc="http://schemas.xmlsoap.org/soap/encoding/" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:cwmp="urn:dslforum-org:cwmp-1-0">
  <soap-env:Header><cwmp:ID soap-env:mustUnderstand="1">acs-poll</cwmp:ID></soap-env:Header>
  <soap-env:Body><cwmp:GetParameterValuesResponse><ParameterList soap-enc:arrayType="cwmp:ParameterValueStruct[2]">
    <ParameterValueStruct><Name>` + pingPrefix + `DiagnosticsState</Name><Value xsi:type="xsd:string">` + state + `</Value></ParameterValueStruct>
    <ParameterValueStruct><Name>` + pingPrefix + `SuccessCount</Name><Value xsi:type="xsd:unsignedInt">4</Value></ParameterValueStruct>
  </ParameterList></cwmp:GetParameterValuesResponse></soap-env:Body>
</soap-env:Envelope>`
}

// TestIntegration_DiagnosticWaitsForLaterSession is the C1 end-to-end gate:
// after the trigger is acknowledged the same session closes without a poll;
// a periodic session polls once ("Requested" parks it again); the
// "8 DIAGNOSTICS COMPLETE" session polls and completes it.
func TestIntegration_DiagnosticWaitsForLaterSession(t *testing.T) {
	h, srv, ctx := newTestGateway(t)
	cpe := &mockCPE{t: t, client: srv.Client(), url: srv.URL + "/cwmp"}
	cpe.post(periodicInform(t))
	cpe.post("")
	list, _ := h.devices.List(ctx, devices.ListParams{})
	device := list.Items[0]

	job, err := h.jobs.CreateWithMaxAttempts(ctx, device.ID, jobs.TypeDiagnosticsPing, jobs.DiagnosticsPingPayload{
		Host: "8.8.8.8", NumberOfRepetitions: 4, Timeout: 5000, DataBlockSize: 64, Prefix: pingPrefix,
	}, "test", jobs.DiagnosticsMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}

	// Session 1: trigger, then nothing more for this job.
	cpe.cookie = nil
	cpe.post(periodicInform(t))
	if code, body := cpe.post(""); code != http.StatusOK || !strings.Contains(body, "<cwmp:SetParameterValues>") {
		t.Fatalf("session 1: %d %s, want trigger SPV", code, body)
	}
	if code, body := cpe.post(fixture(t, "set_parameter_values_response.xml")); code != http.StatusNoContent || body != "" {
		t.Fatalf("session 1 after SPV ack: %d %q, want the session closed without a poll", code, body)
	}
	j, _ := h.jobs.ByID(ctx, job.ID)
	if j.Status != jobs.StatusAwaitingDiagnosticsComplete || j.DiagnosticsDeadline == nil {
		t.Fatalf("after trigger ack: status %s deadline %v, want AWAITING_DIAGNOSTICS_COMPLETE with a deadline", j.Status, j.DiagnosticsDeadline)
	}

	// Session 2 (periodic): exactly one poll; still running -> parked again.
	cpe.cookie = nil
	cpe.post(periodicInform(t))
	if _, body := cpe.post(""); !strings.Contains(body, "<cwmp:GetParameterValues>") {
		t.Fatalf("session 2: %s, want one poll", body)
	}
	if code, _ := cpe.post(pingPollResponse("Requested")); code != http.StatusNoContent {
		t.Fatalf("session 2 after Requested poll: %d, want session closed", code)
	}
	if j, _ = h.jobs.ByID(ctx, job.ID); j.Status != jobs.StatusAwaitingDiagnosticsComplete {
		t.Fatalf("after Requested poll status = %s", j.Status)
	}

	// Session 3 (8 DIAGNOSTICS COMPLETE): poll reads Complete.
	cpe.cookie = nil
	cpe.post(diagnosticsCompleteInform(t))
	if _, body := cpe.post(""); !strings.Contains(body, "<cwmp:GetParameterValues>") {
		t.Fatalf("session 3: %s, want poll", body)
	}
	cpe.post(pingPollResponse("Complete"))
	if j, _ = h.jobs.ByID(ctx, job.ID); j.Status != jobs.StatusSuccess {
		t.Fatalf("after Complete poll status = %s, want SUCCESS", j.Status)
	}
}
