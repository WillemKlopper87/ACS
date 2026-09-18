package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"acs/internal/bss"
)

func TestTranslateActionForDeviceUsesDiscoveredWritablePath(t *testing.T) {
	const primary = "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.PreSharedKey.1.KeyPassphrase"
	const alternate = "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.KeyPassphrase"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/devices/dev-1":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "dev-1", "data_model_root": "IGD1"})
		case "/api/v1/devices/dev-1/parameter-names":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"names":         map[string]bool{primary: false, alternate: true},
				"discovered_at": "2026-09-16T10:00:00Z",
			})
		default:
			t.Fatalf("unexpected ACS request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	h := &handler{acs: bss.NewACSClient(server.URL, time.Second, "")}
	params, err := h.translateActionForDevice(context.Background(), "MODIFY_WIFI", map[string]string{"wifi_password": "safe-value"}, "dev-1")
	if err != nil {
		t.Fatalf("translateActionForDevice: %v", err)
	}
	if len(params) != 1 || params[0].Name != alternate {
		t.Fatalf("parameters = %+v, want selected alternate %q", params, alternate)
	}
}
