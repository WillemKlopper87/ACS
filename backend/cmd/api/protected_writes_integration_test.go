package main

import (
	"net/http"
	"strings"
	"testing"
)

// TestIntegration_ProtectedWritesRefused: no operator-facing path may queue
// a write to a parameter that can cut a CPE off from the ACS
// (parameters.ProtectedReason), and each says why with 403. The
// credential rotation flow, which owns the Connection Request credential,
// still works.
func TestIntegration_ProtectedWritesRefused(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", "superadmin")
	dev := e.device("PROT0001", nil)

	acsURL := []map[string]string{{"name": "Device.ManagementServer.URL", "value": "http://evil.example/acs", "type": "string"}}
	tr69Off := "Device.IP.Interface.2.X_ALU-COM_WanAccessCfg.Tr69Disabled"

	cases := []struct {
		name, method, path string
		body               any
	}{
		{"parameter write", "PUT", "/api/v1/devices/" + dev + "/parameters", map[string]any{"parameters": acsURL}},
		{"bulk action", "POST", "/api/v1/devices/bulk-actions", map[string]any{"action": "SET_PARAMETER", "device_ids": []string{dev}, "parameters": acsURL}},
		{"template", "POST", "/api/v1/config-templates", map[string]any{"name": "repoint", "parameters": []map[string]string{{"name": tr69Off, "value": "1", "type": "boolean"}}}},
		{"policy", "POST", "/api/v1/policies", map[string]any{"name": "no-tr69", "parameter_name": tr69Off, "desired_value": "1"}},
		{"schedule", "POST", "/api/v1/scheduled-jobs", map[string]any{
			"name": "repoint-nightly", "job_type": "SET_PARAMETER", "target_type": "DEVICE", "target_id": dev,
			"payload": map[string]any{"parameters": acsURL}, "interval_seconds": 3600,
		}},
	}
	for _, c := range cases {
		r := e.call("root", c.method, c.path, c.body)
		if r.code != http.StatusForbidden || !strings.Contains(r.body, "protected") {
			t.Errorf("%s: %d %s, want 403 naming the protected parameter", c.name, r.code, r.body)
		}
	}

	var queued int
	if err := e.db.QueryRowContext(e.ctx, `SELECT count(*) FROM jobs WHERE device_id = $1`, dev).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("%d jobs queued by refused writes", queued)
	}

	// Credential rotation owns the Connection Request credential.
	r := e.call("root", "POST", "/api/v1/devices/"+dev+"/credentials/rotate", map[string]string{"credential_type": "CONNECTION_REQUEST"})
	if r.code >= 300 {
		t.Fatalf("credential rotation: %d %s", r.code, r.body)
	}
}
