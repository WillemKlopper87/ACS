package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"acs/internal/bss"
)

func TestInterpretTMF640Patch(t *testing.T) {
	cases := []struct {
		body, action, err string
		params            map[string]string
	}{
		{body: `{"state":"inactive"}`, action: "SUSPEND"},
		{body: `{"state":"active"}`, action: "ACTIVATE"},
		{body: `{"serviceCharacteristic":[{"name":"SSID","value":"Oak"},{"name":"WiFiPassword","value":"s3cret-pass"}]}`, action: "MODIFY_WIFI",
			params: map[string]string{"wifi_ssid": "Oak", "wifi_password": "s3cret-pass"}},
		{body: `{"state":"inactive","serviceCharacteristic":[{"name":"SSID","value":"x"}]}`, err: "not both"},
		{body: `{"state":"paused"}`, err: "active"},
		{body: `{"serviceCharacteristic":[{"name":"Channel","value":"6"}]}`, err: "unsupported"},
		{body: `{"category":"x"}`, err: "only state or serviceCharacteristic"},
		{body: `{}`, err: "no change"},
	}
	for _, c := range cases {
		action, params, err := interpretTMF640Patch([]byte(c.body))
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err = %v, want %q", c.body, err, c.err)
			}
			continue
		}
		if err != nil || action != c.action {
			t.Errorf("%s: %s %v, want %s", c.body, action, err, c.action)
		}
		for k, v := range c.params {
			if params[k] != v {
				t.Errorf("%s: params[%s] = %q, want %q", c.body, k, params[k], v)
			}
		}
	}
}

// fakeACSJobs answers the two ACS calls a TMF640 PATCH makes: the
// parameter write (counting dispatches) and the job read behind a Monitor.
type fakeACSJobs struct {
	mu         sync.Mutex
	dispatches int
	status     string
}

func (f *fakeACSJobs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/parameters"):
		f.dispatches++
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"command_key": "ck-suspend"})
	case strings.HasPrefix(r.URL.Path, "/api/v1/jobs/"):
		_ = json.NewEncoder(w).Encode(bss.JobStatus{CommandKey: "ck-suspend", Status: f.status, FaultString: strPtr("9002 Internal error")})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func strPtr(s string) *string { return &s }

// TestTMF640PatchDispatchesOnceAndMonitorFollowsTheJob: the routes the
// bss-oss-integration ACS adapter calls. A PATCH answers 202 with a
// Monitor; a retried PATCH with the same key dispatches nothing more; the
// Monitor moves InProgress -> Completed/InError with the job.
func TestTMF640PatchDispatchesOnceAndMonitorFollowsTheJob(t *testing.T) {
	acs := &fakeACSJobs{status: "QUEUED"}
	ctx, h, db := newOrderTestHandler(t, acs)
	const accountID, deviceID = "acct-640", "64064064-0640-0640-0640-064064064064"
	seedOrderDevice(t, ctx, db, accountID, deviceID)
	m, err := h.mappings.ActiveDeviceForAccount(ctx, accountID, "gateway")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /tmf-api/serviceActivationAndConfiguration/v4/service/{id}", h.patchService)
	mux.HandleFunc("GET /tmf-api/serviceActivationAndConfiguration/v4/monitor/{id}", h.getMonitor)
	call := func(method, path, key, body string) (int, tmfMonitor, string) {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/merge-patch+json")
		if key != "" {
			req.Header.Set("X-Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var mon tmfMonitor
		_ = json.Unmarshal(rec.Body.Bytes(), &mon)
		return rec.Code, mon, rec.Body.String()
	}
	service := tmfServiceBasePath + m.ID

	if code, _, body := call(http.MethodPatch, service, "", `{"state":"inactive"}`); code != http.StatusBadRequest {
		t.Fatalf("PATCH without X-Idempotency-Key = %d %s", code, body)
	}
	code, mon, body := call(http.MethodPatch, service, "intent-1", `{"state":"inactive"}`)
	if code != http.StatusAccepted || mon.ID != "intent-1" || mon.State != "InProgress" || mon.SourceHref != service {
		t.Fatalf("PATCH = %d %s", code, body)
	}
	if code, again, _ := call(http.MethodPatch, service, "intent-1", `{"state":"inactive"}`); code != http.StatusAccepted || again.ID != "intent-1" {
		t.Fatalf("retried PATCH = %d %+v", code, again)
	}
	if acs.dispatches != 1 {
		t.Fatalf("dispatches = %d, want 1 for a retried PATCH", acs.dispatches)
	}

	monitor := tmfMonitorBasePath + "intent-1"
	if _, mon, _ = call(http.MethodGet, monitor, "", ""); mon.State != "InProgress" {
		t.Fatalf("monitor while queued = %+v", mon)
	}
	acs.mu.Lock()
	acs.status = "SUCCESS"
	acs.mu.Unlock()
	if _, mon, _ = call(http.MethodGet, monitor, "", ""); mon.State != "Completed" || mon.Response == nil || mon.Response.StatusCode != "200" {
		t.Fatalf("monitor after success = %+v", mon)
	}
	acs.mu.Lock()
	acs.status = "FAILED"
	acs.mu.Unlock()
	if _, mon, _ = call(http.MethodGet, monitor, "", ""); mon.State != "InError" || !strings.Contains(mon.Response.Body, "9002") {
		t.Fatalf("monitor after failure = %+v", mon)
	}
	if code, _, _ := call(http.MethodGet, tmfMonitorBasePath+"never-issued", "", ""); code != http.StatusNotFound {
		t.Fatalf("unknown monitor = %d, want 404 (the caller reads it as absent)", code)
	}

	// A key reused for another service is refused, not answered with this one.
	const otherAccount, otherDevice = "acct-other", "64164164-0641-0641-0641-064164164164"
	seedOrderDevice(t, ctx, db, otherAccount, otherDevice)
	other, _ := h.mappings.ActiveDeviceForAccount(ctx, otherAccount, "gateway")
	if code, _, body := call(http.MethodPatch, tmfServiceBasePath+other.ID, "intent-1", `{"state":"inactive"}`); code != http.StatusConflict {
		t.Fatalf("key reused for another service = %d %s", code, body)
	}

	// A pre-provisioned service is reserved until in service; it is not
	// changed before then.
	if _, err := db.ExecContext(ctx, `UPDATE account_device_mappings SET status = 'PENDING_ACTIVE' WHERE id = $1`, other.ID); err != nil {
		t.Fatal(err)
	}
	if code, _, body := call(http.MethodPatch, tmfServiceBasePath+other.ID, "intent-2", `{"state":"inactive"}`); code != http.StatusConflict || !strings.Contains(body, "onboarded") {
		t.Fatalf("PATCH of a reserved service = %d %s", code, body)
	}
	other.Status = bss.StatusPendingActive
	if s := tmf640State(other); s != "reserved" {
		t.Fatalf("state of a pre-provisioned service = %q, want reserved", s)
	}
}
