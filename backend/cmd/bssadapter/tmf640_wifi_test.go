package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"acs/internal/bss"
)

// serviceSSID runs serviceFromMapping against a fake ACS that serves the
// device record and its resolved Wi-Fi view, and returns the SSID and
// SSIDStatus characteristics.
func serviceSSID(t *testing.T, root string, view any, extra http.HandlerFunc) (ssid, status string, present bool) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/devices/dev-1":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "dev-1", "data_model_root": root})
		case "/api/v1/devices/dev-1/wifi/networks":
			if view == nil {
				t.Fatalf("TR-098 device must not use the TR-181 resolver")
			}
			_ = json.NewEncoder(w).Encode(view)
		default:
			if extra == nil {
				t.Fatalf("unexpected ACS request: %s %s", r.Method, r.URL.String())
			}
			extra(w, r)
		}
	}))
	defer server.Close()
	h := &handler{acs: bss.NewACSClient(server.URL, time.Second, "")}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	svc, err := h.serviceFromMapping(req, &bss.AccountDeviceMapping{ID: "svc-1", AccountID: "acct-1", DeviceID: "dev-1"})
	if err != nil {
		t.Fatalf("serviceFromMapping: %v", err)
	}
	for _, c := range svc.ServiceCharacteristic {
		switch c.Name {
		case "SSID":
			ssid, present = c.Value, true
		case "SSIDStatus":
			status = c.Value
		case "WiFiPassword":
			t.Fatalf("WiFiPassword must never be read back")
		}
	}
	return ssid, status, present
}

func TestServiceSSIDFromPrimaryNetworks(t *testing.T) {
	// Nokia FastMile: SENT-5G on SSID.1 (2.4 GHz) and SSID.5 (5 GHz); the
	// guest and disabled factory SSIDs are not in "primary".
	fastMile := map[string]any{"supported": true, "complete": true, "primary": []map[string]any{
		{"instance": 1, "name": "SENT-5G", "band": "2.4GHz"}, {"instance": 5, "name": "SENT-5G", "band": "5GHz"},
	}}
	if ssid, status, _ := serviceSSID(t, "DEVICE2", fastMile, nil); ssid != "SENT-5G" || status != "" {
		t.Errorf("FastMile SSID = %q (status %q), want SENT-5G", ssid, status)
	}

	split := map[string]any{"supported": true, "complete": true, "primary": []map[string]any{
		{"instance": 1, "name": "Home-2G"}, {"instance": 5, "name": "Home-5G"},
	}}
	if ssid, _, _ := serviceSSID(t, "DEVICE2", split, nil); ssid != "Home-2G / Home-5G" {
		t.Errorf("split SSID = %q, want both band names", ssid)
	}
}

func TestServiceSSIDWithoutOwnWiFiCarriesReason(t *testing.T) {
	// Nokia Beacon 3.1: no Device.WiFi. over CWMP (run by the gateway's
	// EasyMesh controller); the ACS view is unsupported with no networks.
	beacon := map[string]any{"supported": false, "complete": true, "networks": []any{}, "primary": nil}
	ssid, status, present := serviceSSID(t, "DEVICE2", beacon, nil)
	if !present || ssid != "" || !strings.Contains(status, "managed by gateway") {
		t.Errorf("Beacon SSID = %q present=%v status=%q, want empty SSID managed by gateway", ssid, present, status)
	}

	incomplete := map[string]any{"supported": true, "complete": false, "primary": []map[string]any{{"instance": 1, "name": "Home"}}}
	if ssid, status, _ := serviceSSID(t, "DEVICE2", incomplete, nil); ssid != "" || !strings.Contains(status, "not read yet") {
		t.Errorf("incomplete view SSID = %q status=%q, want no guess while roles are unknown", ssid, status)
	}
}

func TestServiceSSIDTR098KeepsCanonicalPath(t *testing.T) {
	const path = "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.SSID"
	ssid, _, _ := serviceSSID(t, "IGD1", nil, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/parameters") || r.URL.Query().Get("paths") != path {
			t.Fatalf("unexpected ACS request: %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"parameters": map[string]any{path: map[string]any{"value": "LegacyNet"}}})
	})
	if ssid != "LegacyNet" {
		t.Errorf("TR-098 SSID = %q, want LegacyNet", ssid)
	}
}
