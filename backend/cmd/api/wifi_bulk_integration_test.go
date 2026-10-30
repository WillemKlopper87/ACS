package main

import (
	"encoding/json"
	"strings"
	"testing"

	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/operators"
)

// TestIntegration_BulkSetWiFi drives POST /devices/bulk-actions SET_WIFI
// end to end: a FastMile-shaped device gets one verified job writing
// SSID.1 and SSID.5, a device whose roles are not cached gets the read
// instead, and an undiscovered device is reported, not guessed at.
func TestIntegration_BulkSetWiFi(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", operators.RoleSuperAdmin)
	ready, partial, fresh := e.device("W001", nil), e.device("W002", nil), e.device("W003", nil)

	names, cache := fastMileWiFiTree()
	for _, id := range []string{ready, partial} {
		if err := e.h.devices.UpdateDataModelRoot(e.ctx, id, devices.DataModelRootDevice2); err != nil {
			t.Fatal(err)
		}
		if err := e.h.params.SaveNames(e.ctx, id, names); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.h.params.Upsert(e.ctx, ready, cache); err != nil {
		t.Fatal(err)
	}
	delete(cache, "Device.WiFi.SSID.6.X_ALU-COM_IsGuestSSID")
	if err := e.h.params.Upsert(e.ctx, partial, cache); err != nil {
		t.Fatal(err)
	}

	r := e.call("root", "POST", "/api/v1/devices/bulk-actions", map[string]any{
		"action": "SET_WIFI", "device_ids": []string{ready, partial, fresh}, "wifi_ssid": "NewHome",
	})
	if r.code != 202 {
		t.Fatalf("bulk SET_WIFI → %d %s", r.code, r.body)
	}
	var out struct {
		Succeeded int                `json:"succeeded"`
		Results   []bulkActionResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Succeeded != 1 || len(out.Results) != 3 {
		t.Fatalf("results = %+v", out)
	}
	byID := map[string]bulkActionResult{}
	for _, res := range out.Results {
		byID[res.DeviceID] = res
	}

	ok := byID[ready]
	if ok.Error != "" || ok.CommandKey == "" || ok.Detail != "SSID 1 (2.4GHz) + SSID 5 (5GHz)" {
		t.Fatalf("ready device result = %+v", ok)
	}
	list, err := e.h.jobs.List(e.ctx, ready, nil, false)
	if err != nil || len(list) != 1 {
		t.Fatalf("jobs for ready device = %v, %v", list, err)
	}
	var payload jobs.SetParameterPayload
	_ = json.Unmarshal(list[0].Payload, &payload)
	var written []string
	for _, p := range payload.Parameters {
		written = append(written, p.Name)
	}
	if list[0].Type != jobs.TypeSetParameter || !payload.Verify || strings.Join(written, ",") != "Device.WiFi.SSID.1.SSID,Device.WiFi.SSID.5.SSID" {
		t.Errorf("queued job = %s verify=%v writes=%v", list[0].Type, payload.Verify, written)
	}

	pending := byID[partial]
	if pending.CommandKey != "" || pending.RefreshCommandKey == "" || !strings.Contains(pending.Error, "roles not known") {
		t.Errorf("incomplete device result = %+v, want a queued read and an error", pending)
	}
	if list, _ := e.h.jobs.List(e.ctx, partial, nil, false); len(list) != 1 || list[0].Type != jobs.TypeGetParameter {
		t.Errorf("incomplete device must get exactly the refresh read, got %+v", list)
	}

	if res := byID[fresh]; !strings.Contains(res.Error, "discovery") {
		t.Errorf("undiscovered device result = %+v, want a discovery error", res)
	}

	if r := e.call("root", "POST", "/api/v1/devices/bulk-actions", map[string]any{"action": "SET_WIFI", "device_ids": []string{ready}}); r.code != 400 {
		t.Errorf("SET_WIFI without settings → %d, want 400", r.code)
	}
}
