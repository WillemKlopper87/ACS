package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"testing"
)

// operation posts a device operation as the BSS adapter does: the service
// token, no body, an Idempotency-Key. It returns the status and the
// queued job's command key and parameter paths.
func (e *testEnv) operation(path, key string) (int, string, []string) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+testServiceToken)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	defer r.Body.Close()
	var out struct {
		CommandKey string   `json:"command_key"`
		Parameters []string `json:"parameters"`
	}
	json.NewDecoder(r.Body).Decode(&out)
	return r.StatusCode, out.CommandKey, out.Parameters
}

// The BSS adapter's order outbox retries a dispatch whose answer was lost.
// With the order's Idempotency-Key the retry gets the job already queued:
// a router is not restarted, or wiped, twice for one order.
func TestIntegration_RebootAndFactoryResetAreIdempotentPerKey(t *testing.T) {
	e := newTestEnv(t)
	dev := e.device("OP001", nil)
	for _, op := range []string{"reboot", "factory-reset"} {
		path := "/api/v1/devices/" + dev + "/" + op
		code, first, _ := e.operation(path, "bss:ord-1:operation")
		if code != http.StatusAccepted || first == "" {
			t.Fatalf("%s: status = %d, command_key = %q", op, code, first)
		}
		if code, again, _ := e.operation(path, "bss:ord-1:operation"); code != http.StatusAccepted || again != first {
			t.Errorf("%s retried with the same key: status = %d, command_key = %q, want %q", op, code, again, first)
		}
		if _, other, _ := e.operation(path, "bss:ord-2:operation"); other == first {
			t.Errorf("%s with another key must queue another job", op)
		}
		if _, plain, _ := e.operation(path, ""); plain == first {
			t.Errorf("%s without a key must queue its own job", op)
		}
	}
	if code, _, _ := e.operation("/api/v1/devices/00000000-0000-0000-0000-000000000000/reboot", "k"); code != http.StatusNotFound {
		t.Errorf("unknown device: status = %d, want 404", code)
	}
}

// refresh-status asks for what the device is known to have, in ONE job.
func TestIntegration_RefreshStatusAsksOnlyForKnownParameters(t *testing.T) {
	e := newTestEnv(t)
	dev := e.device("OP002", nil)
	path := "/api/v1/devices/" + dev + "/parameters/refresh-status"

	// No discovery yet: the uptime of the device's data model, alone.
	code, first, paths := e.operation(path, "bss:ord-s1:operation")
	if code != http.StatusAccepted || len(paths) != 1 || paths[0] != "Device.DeviceInfo.UpTime" {
		t.Fatalf("undiscovered: status = %d, paths = %v", code, paths)
	}
	if _, again, _ := e.operation(path, "bss:ord-s1:operation"); again != first {
		t.Errorf("retried with the same key: command_key = %q, want %q", again, first)
	}

	// Discovered: only the status parameters the device reported.
	if err := e.h.params.SaveNames(e.ctx, dev, map[string]bool{
		"Device.DeviceInfo.UpTime":         false,
		"Device.Hosts.HostNumberOfEntries": false,
		"Device.WiFi.SSID.1.SSID":          true,
	}); err != nil {
		t.Fatalf("save names: %v", err)
	}
	code, _, paths = e.operation(path, "bss:ord-s2:operation")
	sort.Strings(paths)
	if code != http.StatusAccepted || len(paths) != 2 || paths[0] != "Device.DeviceInfo.UpTime" || paths[1] != "Device.Hosts.HostNumberOfEntries" {
		t.Errorf("discovered: status = %d, paths = %v", code, paths)
	}

	// The first key again, now that discovery found more: still the same
	// request (the caller sends no parameter list), so the job it queued.
	if code, again, _ := e.operation(path, "bss:ord-s1:operation"); code != http.StatusAccepted || again != first {
		t.Errorf("resent after discovery: status = %d, command_key = %q, want 202 and %q", code, again, first)
	}

	// A device that reported none of them cannot answer: 409, no job.
	bare := e.device("OP003", nil)
	if err := e.h.params.SaveNames(e.ctx, bare, map[string]bool{"Device.WiFi.SSID.1.SSID": true}); err != nil {
		t.Fatalf("save names: %v", err)
	}
	if code, key, _ := e.operation("/api/v1/devices/"+bare+"/parameters/refresh-status", "bss:ord-s3:operation"); code != http.StatusConflict || key != "" {
		t.Errorf("no status parameter: status = %d, command_key = %q, want 409 and none", code, key)
	}
}
