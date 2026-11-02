package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/operators"
	"acs/internal/parameters"
	"acs/internal/tenancy"
)

// fastMileWiFiEditTree is fastMileWiFiTree plus the AccessPoint switches
// the per-network editor writes (enable, broadcast, security mode).
func fastMileWiFiEditTree() (map[string]bool, map[string]parameters.CachedValue) {
	names, cache := fastMileWiFiTree()
	for i := 1; i <= 8; i++ {
		ap := "Device.WiFi.AccessPoint." + strconv.Itoa(i) + "."
		enabled := cache["Device.WiFi.SSID."+strconv.Itoa(i)+".Enable"].Value
		names[ap+"Enable"] = true
		cache[ap+"Enable"] = parameters.CachedValue{Value: enabled}
		names[ap+"SSIDAdvertisementEnabled"] = true
		cache[ap+"SSIDAdvertisementEnabled"] = parameters.CachedValue{Value: "1"}
		names[ap+"Security.ModeEnabled"] = true
		cache[ap+"Security.ModeEnabled"] = parameters.CachedValue{Value: "WPA2-Personal"}
		names[ap+"Security.ModesSupported"] = false
		cache[ap+"Security.ModesSupported"] = parameters.CachedValue{Value: "None,WPA2-Personal,WPA3-Personal,WPA2-WPA3-Personal"}
	}
	return names, cache
}

func TestValidPassphrase(t *testing.T) {
	for p, ok := range map[string]bool{
		"short":                      false,
		"long enough":                true,
		strings.Repeat("a", 63):      true,
		strings.Repeat("a", 65):      false,
		strings.Repeat("0f", 32):     true,
		strings.Repeat("zz", 32):     false,
		"tab\tinside":                false,
		"café au lait not ascii ok?": false,
	} {
		if err := validPassphrase(p); (err == nil) != ok {
			t.Errorf("validPassphrase(%q) = %v, want ok=%v", p, err, ok)
		}
	}
}

// TestIntegration_WiFiNetworkEdit drives PUT /devices/{id}/wifi/networks/{n}
// on a FastMile-shaped device: one network at a time, name and passphrase
// in one write, and the guards against open or switched-off Wi-Fi.
func TestIntegration_WiFiNetworkEdit(t *testing.T) {
	e := newTestEnv(t)
	cust := e.customer("ISP A")
	e.operator("root", operators.RoleSuperAdmin)
	e.operator("viewer", operators.RoleReadOnly, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: cust})
	dev := e.device("WE01", &cust)
	if err := e.h.devices.UpdateDataModelRoot(e.ctx, dev, devices.DataModelRootDevice2); err != nil {
		t.Fatal(err)
	}
	names, cache := fastMileWiFiEditTree()
	if err := e.h.params.SaveNames(e.ctx, dev, names); err != nil {
		t.Fatal(err)
	}
	if err := e.h.params.Upsert(e.ctx, dev, cache); err != nil {
		t.Fatal(err)
	}
	path := func(n int) string { return "/api/v1/devices/" + dev + "/wifi/networks/" + strconv.Itoa(n) }
	lastWrites := func() []jobs.ParameterWrite {
		t.Helper()
		list, err := e.h.jobs.List(e.ctx, dev, nil, false)
		if err != nil || len(list) == 0 {
			t.Fatalf("jobs = %v, %v", list, err)
		}
		var p jobs.SetParameterPayload
		_ = json.Unmarshal(list[0].Payload, &p)
		if !p.Verify {
			t.Error("Wi-Fi changes must be verified by read-back")
		}
		return p.Parameters
	}

	// The view carries the editor's fields.
	r := e.call("root", "GET", "/api/v1/devices/"+dev+"/wifi/networks", nil)
	if r.code != 200 || !strings.Contains(r.body, `"security_mode_writable":true`) || !strings.Contains(r.body, `"modes_supported":["None","WPA2-Personal"`) {
		t.Fatalf("view = %d %s", r.code, r.body)
	}

	// Rename only the 2.4 GHz primary, with a new passphrase, in one write.
	r = e.call("root", "PUT", path(1), map[string]any{"name": "Home-2G", "passphrase": "correct horse battery"})
	if r.code != 202 || !strings.Contains(r.body, `"disconnects":true`) {
		t.Fatalf("rename SSID 1 = %d %s", r.code, r.body)
	}
	writes := lastWrites()
	if len(writes) != 2 || writes[0].Name != "Device.WiFi.SSID.1.SSID" || writes[0].Value != "Home-2G" || writes[1].Name != "Device.WiFi.AccessPoint.1.Security.KeyPassphrase" {
		t.Fatalf("writes = %+v", writes)
	}

	// The paired 5 GHz network in the same change.
	r = e.call("root", "PUT", path(1), map[string]any{"name": "Home", "also_instances": []int{5}})
	if r.code != 202 {
		t.Fatalf("rename both bands = %d %s", r.code, r.body)
	}
	if w := lastWrites(); len(w) != 2 || w[1].Name != "Device.WiFi.SSID.5.SSID" {
		t.Fatalf("paired writes = %+v", w)
	}

	// Turning on a spare network needs its passphrase.
	if r := e.call("root", "PUT", path(3), map[string]any{"name": "Office", "enabled": true}); r.code != 400 || !strings.Contains(r.body, "passphrase") {
		t.Fatalf("enable spare without passphrase = %d %s", r.code, r.body)
	}
	r = e.call("root", "PUT", path(3), map[string]any{"name": "Office", "passphrase": "office-pass-2026", "security_mode": "WPA3-Personal", "enabled": true})
	if r.code != 202 || strings.Contains(r.body, `"disconnects":true`) {
		t.Fatalf("enable spare = %d %s", r.code, r.body)
	}
	got := map[string]string{}
	for _, w := range lastWrites() {
		got[w.Name] = w.Value
	}
	if got["Device.WiFi.SSID.3.Enable"] != "1" || got["Device.WiFi.AccessPoint.3.Enable"] != "1" || got["Device.WiFi.AccessPoint.3.Security.ModeEnabled"] != "WPA3-Personal" {
		t.Fatalf("spare writes = %v", got)
	}

	// Guards.
	for name, tc := range map[string]struct {
		n    int
		body map[string]any
		code int
	}{
		"open without confirmation": {1, map[string]any{"security_mode": "None"}, 400},
		"unsupported mode":          {1, map[string]any{"security_mode": "WEP-64"}, 400},
		"short passphrase":          {1, map[string]any{"passphrase": "short"}, 400},
		"name too long":             {1, map[string]any{"name": strings.Repeat("x", 33)}, 400},
		"nothing to change":         {1, map[string]any{}, 400},
		"unknown field":             {1, map[string]any{"ssid": "x"}, 400},
		"no such network":           {9, map[string]any{"name": "x"}, 404},
		"too many also_instances":   {1, map[string]any{"name": "x", "also_instances": make([]int, 17)}, 400},
		"every network off":         {1, map[string]any{"enabled": false, "also_instances": []int{2, 5, 6}}, 409},
	} {
		if r := e.call("root", "PUT", path(tc.n), tc.body); r.code != tc.code {
			t.Errorf("%s = %d %s, want %d", name, r.code, r.body, tc.code)
		}
	}
	if r := e.call("root", "PUT", path(1), map[string]any{"security_mode": "None", "allow_open": true}); r.code != 202 {
		t.Errorf("confirmed open network = %d %s", r.code, r.body)
	}
	if r := e.call("viewer", "PUT", path(1), map[string]any{"name": "x"}); r.code != 403 {
		t.Errorf("readonly operator = %d, want 403", r.code)
	}
}
