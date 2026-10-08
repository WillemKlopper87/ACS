package main

import (
	"acs/internal/bulkdata"
	"acs/internal/operators"
	"acs/internal/parameters"
	"acs/internal/tenancy"
	"acs/internal/transfer"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIntegration_CompatibilityEvidenceAndReceiverIsolation(t *testing.T) {
	e := newTestEnv(t)
	a, b := e.customer("A"), e.customer("B")
	device := e.device("COMPAT-A", &a)
	e.operator("alice", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: a})
	e.operator("bob", operators.RoleNOC, tenancy.Scope{Type: tenancy.ScopeCustomer, ID: b})
	for _, endpoint := range []string{"compatibility", "instances", "5g-telemetry", "telemetry-health", "discovery-export"} {
		if got := e.call("bob", "GET", "/api/v1/devices/"+device+"/"+endpoint, nil); got.code != 404 {
			t.Fatal("cross-tenant evidence leaked", endpoint, got)
		}
	}
	version := "Device.DeviceInfo.SoftwareVersion"
	if err := e.h.params.Upsert(e.ctx, device, map[string]parameters.CachedValue{version: {Value: "1.0", UpdatedAt: time.Now(), Source: parameters.SourceInform}}); err != nil {
		t.Fatal(err)
	}
	if err := e.h.params.RecordOperation(e.ctx, device, "read-evidence", "READ", true, []string{version}, map[string]any{}, "1.0"); err != nil {
		t.Fatal(err)
	}
	if got := e.call("alice", "GET", "/api/v1/devices/"+device+"/compatibility", nil); got.code != 200 || !strings.Contains(got.body, `"read_tested":true`) {
		t.Fatal(got)
	}
	receiver, err := e.h.bulkData.Create(e.ctx, device, 1, []string{version}, time.Now().Add(time.Hour), 60)
	if err != nil {
		t.Fatal(err)
	}
	token := transfer.Sign(e.h.transferKey, "bulk-data", receiver.ID, receiver.ExpiresAt)
	send := func(raw []byte, compressed bool) int {
		t.Helper()
		body := raw
		if compressed {
			var buffer bytes.Buffer
			writer := gzip.NewWriter(&buffer)
			_, _ = writer.Write(raw)
			_ = writer.Close()
			body = buffer.Bytes()
		}
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/v1/bulk-data/"+receiver.ID+"/receive", bytes.NewReader(body))
		req.SetBasicAuth(receiver.ID, token)
		req.Header.Set("Content-Type", "application/json")
		if compressed {
			req.Header.Set("Content-Encoding", "gzip")
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	raw := []byte(`{"Report":[{"CollectionTime":"2026-10-06T12:00:00Z","Device.DeviceInfo.SoftwareVersion":"1.0"}]}`)
	if send(raw, true) != 204 || send(raw, false) != 204 {
		t.Fatal("valid or duplicate report rejected")
	}
	reports, err := e.h.bulkData.Reports(e.ctx, device)
	if err != nil || len(reports) != 1 {
		t.Fatal("dedup failed", reports, err)
	}
	if send([]byte(`{"Report":[{"Device.WiFi.SSID.1.SSID":"other"}]}`), false) != 400 {
		t.Fatal("unconfigured metric accepted")
	}
	if send(bytes.Repeat([]byte("x"), 1<<20+1), true) != 413 {
		t.Fatal("oversized decompression accepted")
	}
	health, err := e.h.bulkData.Health(e.ctx, device, time.Now())
	if err != nil || len(health) != 1 || health[0].State != "receiving" {
		t.Fatal(health, err)
	}
	if err := e.h.bulkData.Revoke(e.ctx, receiver.ID, device); err != nil {
		t.Fatal(err)
	}
	if send(raw, false) != 403 {
		t.Fatal("revoked receiver accepted")
	}
	if err := bulkdata.ValidateReport(raw, []string{version}); err != nil {
		t.Fatal(err)
	}
}
