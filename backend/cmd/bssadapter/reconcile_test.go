package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/bss"
)

// acsRouterStub answers GET /api/v1/jobs/{command_key} and GET
// /api/v1/devices/{id}/parameters -- the two ACS endpoints getReconciliation
// calls -- from fixed maps, the shape every real cmd/api instance would
// return for a confirmed job and a device's current parameter cache.
func acsRouterStub(t *testing.T, jobStatus map[string]string, parameters map[string]bss.CachedParameter) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/jobs/"):
			commandKey := strings.TrimPrefix(r.URL.Path, "/api/v1/jobs/")
			status, ok := jobStatus[commandKey]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(bss.JobStatus{CommandKey: commandKey, Status: status})
		case strings.Contains(r.URL.Path, "/parameters"):
			_ = json.NewEncoder(w).Encode(map[string]any{"parameters": parameters})
		default:
			t.Errorf("unexpected ACS request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func insertDispatchedOrder(t *testing.T, ctx context.Context, r *bss.Repository, externalID, accountID, deviceID, commandKey string, params []bss.ParameterWrite) {
	t.Helper()
	if err := r.InsertPending(ctx, externalID, accountID, "MODIFY_WIFI", deviceID, params); err != nil {
		t.Fatalf("InsertPending %s: %v", externalID, err)
	}
	if err := r.MarkDispatched(ctx, externalID, commandKey); err != nil {
		t.Fatalf("MarkDispatched %s: %v", externalID, err)
	}
}

// TestGetReconciliationReportsMatchAndDrift is the end-to-end path: two
// confirmed orders against different paths, one matching the device's
// current state and one drifted, surfaced in a single response.
func TestGetReconciliationReportsMatchAndDrift(t *testing.T) {
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	ctx, h, db := newOrderTestHandler(t, acsRouterStub(t,
		map[string]string{"ck-ssid": "SUCCESS", "ck-enable": "SUCCESS"},
		map[string]bss.CachedParameter{
			"Device.WiFi.SSID.1.SSID":   {Value: "HomeNet"},
			"Device.WiFi.SSID.1.Enable": {Value: "0"},
		},
	))
	seedOrderDevice(t, ctx, db, accountID, deviceID)
	insertDispatchedOrder(t, ctx, h.mappings, "ord-ssid", accountID, deviceID, "ck-ssid",
		[]bss.ParameterWrite{{Name: "Device.WiFi.SSID.1.SSID", Value: "HomeNet", Type: "string"}})
	insertDispatchedOrder(t, ctx, h.mappings, "ord-enable", accountID, deviceID, "ck-enable",
		[]bss.ParameterWrite{{Name: "Device.WiFi.SSID.1.Enable", Value: "1", Type: "boolean"}})

	req := httptest.NewRequest(http.MethodGet, "/bss/v1/devices/"+deviceID+"/reconciliation", nil)
	req.SetPathValue("device_id", deviceID)
	rec := httptest.NewRecorder()
	h.getReconciliation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var got bss.DeviceReconciliation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.AccountID != accountID || len(got.Fields) != 2 {
		t.Fatalf("got = %+v, want account %s and 2 fields", got, accountID)
	}
	byPath := map[string]bss.ReconciliationField{}
	for _, f := range got.Fields {
		byPath[f.Path] = f
	}
	if byPath["Device.WiFi.SSID.1.SSID"].Status != bss.ReconcileMatch {
		t.Errorf("SSID status = %+v, want match", byPath["Device.WiFi.SSID.1.SSID"])
	}
	if byPath["Device.WiFi.SSID.1.Enable"].Status != bss.ReconcileDrift {
		t.Errorf("Enable status = %+v, want drift (ordered 1, device reports 0)", byPath["Device.WiFi.SSID.1.Enable"])
	}
}

func TestGetReconciliationUsesCurrentAccountAndFiltersHistory(t *testing.T) {
	const deviceID = "11111111-1111-1111-1111-111111111111"
	ctx, h, db := newOrderTestHandler(t, acsRouterStub(t,
		map[string]string{"ck-old": "SUCCESS", "ck-current": "SUCCESS"},
		map[string]bss.CachedParameter{"Device.WiFi.SSID.1.SSID": {Value: "CurrentNet"}},
	))
	h.oauthSigningSecret = []byte("reconciliation-test-oauth-secret-material")
	seedOrderDevice(t, ctx, db, "acct-old", deviceID)
	insertDispatchedOrder(t, ctx, h.mappings, "old-order", "acct-old", deviceID, "ck-old",
		[]bss.ParameterWrite{{Name: "Device.WiFi.SSID.1.SSID", Value: "FormerNet", Type: "string"}})
	if _, err := db.ExecContext(ctx, `UPDATE account_device_mappings SET unassigned_at = now(), unassign_reason = 'moved' WHERE device_id = $1 AND unassigned_at IS NULL`, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, role, assigned_at) VALUES (gen_random_uuid(), 'acct-current', $1, $2, 'gateway', now())`, deviceID, deviceID+"-serial"); err != nil {
		t.Fatal(err)
	}
	insertDispatchedOrder(t, ctx, h.mappings, "current-order", "acct-current", deviceID, "ck-current",
		[]bss.ParameterWrite{{Name: "Device.WiFi.SSID.1.SSID", Value: "CurrentNet", Type: "string"}})

	issueToken := func(accountID string) string {
		t.Helper()
		now := time.Now()
		token, err := auth.SignJWT(h.oauthSigningSecret, auth.Claims{Subject: "client", Role: bssClientRole,
			IssuedAt: now, ExpiresAt: now.Add(time.Hour), AccountIDs: []string{accountID}})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	for _, tc := range []struct {
		accountID  string
		wantStatus int
	}{{"acct-old", http.StatusNotFound}, {"acct-current", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodGet, "/bss/v1/devices/"+deviceID+"/reconciliation", nil)
		req.Header.Set("Authorization", "Bearer "+issueToken(tc.accountID))
		req.SetPathValue("device_id", deviceID)
		rec := httptest.NewRecorder()
		h.getReconciliation(rec, req)
		if rec.Code != tc.wantStatus {
			t.Fatalf("account %s status = %d, want %d; body: %s", tc.accountID, rec.Code, tc.wantStatus, rec.Body.String())
		}
		if tc.wantStatus != http.StatusOK {
			continue
		}
		var got bss.DeviceReconciliation
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if got.AccountID != "acct-current" || len(got.Fields) != 1 || got.Fields[0].IntendedValue != "CurrentNet" {
			t.Fatalf("reconciliation = %+v, want only current account intent", got)
		}
	}
}

// TestGetReconciliationUnknownDeviceIsNotFound proves a device id with no
// BSS order history 404s rather than 200-with-an-empty-list -- it can't be
// used to enumerate real device ids that simply were never BSS-managed.
func TestGetReconciliationUnknownDeviceIsNotFound(t *testing.T) {
	_, h, _ := newOrderTestHandler(t, acsRouterStub(t, nil, nil))

	const unknownDeviceID = "99999999-9999-9999-9999-999999999999"
	req := httptest.NewRequest(http.MethodGet, "/bss/v1/devices/"+unknownDeviceID+"/reconciliation", nil)
	req.SetPathValue("device_id", unknownDeviceID)
	rec := httptest.NewRecorder()
	h.getReconciliation(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

// TestGetReconciliationMalformedDeviceIDIsNotFound proves a device_id that
// isn't even a valid UUID also 404s -- not a 500 from a raw Postgres UUID
// cast error, and not a distinguishable response from "well-formed but
// unknown" above, since a status difference there would itself be a
// (weak) format-validity oracle.
func TestGetReconciliationMalformedDeviceIDIsNotFound(t *testing.T) {
	_, h, _ := newOrderTestHandler(t, acsRouterStub(t, nil, nil))

	req := httptest.NewRequest(http.MethodGet, "/bss/v1/devices/not-a-uuid/reconciliation", nil)
	req.SetPathValue("device_id", "not-a-uuid")
	rec := httptest.NewRecorder()
	h.getReconciliation(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

// TestGetReconciliationUnconfirmedJobIsNotCompared proves a job that
// hasn't reached SUCCESS (still RPC_SENT, or FAILED) is reported
// unconfirmed rather than compared, even though the ACS parameters stub
// here would otherwise "match" -- see reconcile_test.go's own
// TestComputeReconciliationUnconfirmedOrderIsNeverCompared for the pure
// version of this property; this proves the HTTP handler actually wires
// GetJobStatus's result into that check.
func TestGetReconciliationUnconfirmedJobIsNotCompared(t *testing.T) {
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	ctx, h, db := newOrderTestHandler(t, acsRouterStub(t,
		map[string]string{"ck-ssid": "RPC_SENT"},
		map[string]bss.CachedParameter{"Device.WiFi.SSID.1.SSID": {Value: "HomeNet"}},
	))
	seedOrderDevice(t, ctx, db, accountID, deviceID)
	insertDispatchedOrder(t, ctx, h.mappings, "ord-ssid", accountID, deviceID, "ck-ssid",
		[]bss.ParameterWrite{{Name: "Device.WiFi.SSID.1.SSID", Value: "HomeNet", Type: "string"}})

	req := httptest.NewRequest(http.MethodGet, "/bss/v1/devices/"+deviceID+"/reconciliation", nil)
	req.SetPathValue("device_id", deviceID)
	rec := httptest.NewRecorder()
	h.getReconciliation(rec, req)

	var got bss.DeviceReconciliation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Fields) != 1 || got.Fields[0].Status != bss.ReconcileUnconfirmed {
		t.Fatalf("fields = %+v, want a single unconfirmed field", got.Fields)
	}
}
