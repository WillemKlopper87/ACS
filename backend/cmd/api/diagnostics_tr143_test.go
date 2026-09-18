package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"acs/internal/operators"
)

// seedTR143Capability records a discovered-parameter-names row advertising
// both TR-143 diagnostic objects, the way a real ParameterDiscovery job's
// result would — ResolveTR143Capability only recognizes a capability the
// device actually discovered, never a static guess.
func seedTR143Capability(t *testing.T, e *testEnv, deviceID string) {
	t.Helper()
	if err := e.h.params.SaveNames(e.ctx, deviceID, map[string]bool{
		"Device.IP.Diagnostics.DownloadDiagnostics.": true,
		"Device.IP.Diagnostics.UploadDiagnostics.":   true,
	}); err != nil {
		t.Fatalf("seed TR-143 capability: %v", err)
	}
}

// TestCreateTR143QueuesTypedPayload proves a valid download request
// creates a DIAGNOSTICS_DOWNLOAD job carrying the URL and the device's
// discovered DownloadPath as prefix — the typed jobs.DiagnosticsDownloadPayload,
// not the map[string]string this handler used before.
func TestCreateTR143QueuesTypedPayload(t *testing.T) {
	t.Setenv("ACS_TR143_ALLOWED_HOSTS", "speedtest.example.com")
	e := newTestEnv(t)
	deviceID := e.device("TR143-1", nil)
	seedTR143Capability(t, e, deviceID)
	e.operator("alice", operators.RoleSuperAdmin)

	r := e.call("alice", http.MethodPost, "/api/v1/devices/"+deviceID+"/diagnostics/tr143/download",
		map[string]string{"url": "https://speedtest.example.com/100MB.bin"})
	if r.code != http.StatusAccepted {
		t.Fatalf("create download -> %d %s", r.code, r.body)
	}

	jobs, err := e.h.jobs.List(e.ctx, deviceID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Type != "DIAGNOSTICS_DOWNLOAD" {
		t.Fatalf("jobs = %+v, want exactly one DIAGNOSTICS_DOWNLOAD job", jobs)
	}
	var payload struct {
		URL    string `json:"url"`
		Prefix string `json:"prefix"`
	}
	if err := json.Unmarshal(jobs[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.URL != "https://speedtest.example.com/100MB.bin" || payload.Prefix != "Device.IP.Diagnostics.DownloadDiagnostics." {
		t.Fatalf("payload = %+v, want the requested URL and the discovered download prefix", payload)
	}
}

// TestCreateTR143RejectsUnknownDirection is the regression test for a real
// bug this polish pass fixed: {direction} is a free-form path segment, and
// the handler used to treat anything other than the literal string
// "upload" as "download" -- so a typo like "downlaod" silently ran a
// download test instead of failing loudly.
func TestCreateTR143RejectsUnknownDirection(t *testing.T) {
	t.Setenv("ACS_TR143_ALLOWED_HOSTS", "speedtest.example.com")
	e := newTestEnv(t)
	deviceID := e.device("TR143-2", nil)
	seedTR143Capability(t, e, deviceID)
	e.operator("alice", operators.RoleSuperAdmin)

	r := e.call("alice", http.MethodPost, "/api/v1/devices/"+deviceID+"/diagnostics/tr143/downlaod",
		map[string]string{"url": "https://speedtest.example.com/100MB.bin"})
	if r.code != http.StatusBadRequest {
		t.Fatalf("misspelled direction -> %d %s, want 400", r.code, r.body)
	}

	jobs, err := e.h.jobs.List(e.ctx, deviceID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs = %+v, want none queued for a rejected request", jobs)
	}
}

// TestCreateTR143RejectsUndiscoveredCapability proves a device that never
// discovered the upload object (only download, as seeded here) is refused
// for an upload request rather than silently queuing a job the CPE never
// advertised support for.
func TestCreateTR143RejectsUndiscoveredCapability(t *testing.T) {
	t.Setenv("ACS_TR143_ALLOWED_HOSTS", "speedtest.example.com")
	e := newTestEnv(t)
	deviceID := e.device("TR143-3", nil)
	if err := e.h.params.SaveNames(e.ctx, deviceID, map[string]bool{
		"Device.IP.Diagnostics.DownloadDiagnostics.": true,
	}); err != nil {
		t.Fatal(err)
	}
	e.operator("alice", operators.RoleSuperAdmin)

	r := e.call("alice", http.MethodPost, "/api/v1/devices/"+deviceID+"/diagnostics/tr143/upload",
		map[string]string{"url": "https://speedtest.example.com/upload"})
	if r.code != http.StatusConflict {
		t.Fatalf("undiscovered upload capability -> %d %s, want 409", r.code, r.body)
	}
}
