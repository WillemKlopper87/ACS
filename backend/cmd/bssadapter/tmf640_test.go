package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"acs/internal/bss"
	"acs/internal/observability"
	"acs/internal/store"
)

// newTMF640TestHandler mirrors order_test.go's newOrderTestHandler: a
// clean, fully migrated schema per test, wired into just enough of
// bssadapter's handler to exercise the TMF640 endpoints, plus an
// httptest ACS backend for GetDevice/GetParameters/SetParameters.
func newTMF640TestHandler(t *testing.T, acsHandler http.Handler) (context.Context, *handler, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	acsServer := httptest.NewServer(acsHandler)
	t.Cleanup(acsServer.Close)

	return ctx, &handler{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		mappings:     bss.NewRepository(db),
		acs:          bss.NewACSClient(acsServer.URL, 0, ""),
		auditor:      observability.NewAuditor(db),
		walledGarden: bss.WalledGardenConfig{Parameter: "Device.X_WALLED_GARDEN.Enable", SuspendValue: "true", ActiveValue: "false"},
	}, db
}

func seedTMF640Device(t *testing.T, ctx context.Context, db *sql.DB, accountID, deviceID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ($1, $2)`, deviceID, deviceID+"-serial"); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO account_device_mappings (id, account_id, device_id, oui_serial, role, assigned_at) VALUES (gen_random_uuid(), $1, $2, $3, 'gateway', now())`,
		accountID, deviceID, deviceID+"-serial"); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
}

func tmf640ACSHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path != "" && !contains(r.URL.Path, "parameters"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"id": "dev-1", "data_model_root": "DEVICE2"})
		case r.Method == http.MethodGet && contains(r.URL.Path, "parameters"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"parameters": map[string]any{
					"Device.WiFi.SSID.1.SSID":                          map[string]any{"value": "MyNetwork", "type": "string", "updated_at": "2026-09-13T10:00:00Z", "source": "GET_PARAMETER_VALUES"},
					"Device.WiFi.AccessPoint.1.Security.KeyPassphrase": map[string]any{"value": "s3cr3t", "type": "string", "updated_at": "2026-09-13T10:00:00Z", "source": "GET_PARAMETER_VALUES"},
				},
			})
		default:
			t.Fatalf("unexpected ACS request: %s %s", r.Method, r.URL.Path)
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestGetServiceReturnsCurrentCharacteristics(t *testing.T) {
	ctx, h, db := newTMF640TestHandler(t, tmf640ACSHandler(t))
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedTMF640Device(t, ctx, db, accountID, deviceID)

	var mappingID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM account_device_mappings WHERE account_id = $1`, accountID).Scan(&mappingID); err != nil {
		t.Fatalf("read seeded mapping id: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceActivationAndConfiguration/v4/service/"+mappingID, nil)
	req.SetPathValue("id", mappingID)
	rec := httptest.NewRecorder()
	h.getService(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var svc tmfService
	if err := json.Unmarshal(rec.Body.Bytes(), &svc); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if svc.ID != mappingID {
		t.Errorf("ID = %q, want %q", svc.ID, mappingID)
	}
	if svc.State != "active" {
		t.Errorf("State = %q, want active", svc.State)
	}
	found := map[string]string{}
	for _, c := range svc.ServiceCharacteristic {
		found[c.Name] = c.Value
	}
	if found["SSID"] != "MyNetwork" {
		t.Errorf("ServiceCharacteristic SSID = %q, want MyNetwork", found["SSID"])
	}
	if _, present := found["WiFiPassword"]; present {
		t.Errorf("ServiceCharacteristic must never include WiFiPassword on a read (security decision -- GET redacts it): got %+v", svc.ServiceCharacteristic)
	}
}

func TestGetServiceNotFound(t *testing.T) {
	ctx, h, _ := newTMF640TestHandler(t, tmf640ACSHandler(t))
	req := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceActivationAndConfiguration/v4/service/99999999-9999-9999-9999-999999999999", nil)
	req = req.WithContext(ctx)
	req.SetPathValue("id", "99999999-9999-9999-9999-999999999999")
	rec := httptest.NewRecorder()
	h.getService(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestListServicesRequiresAccountID(t *testing.T) {
	_, h, _ := newTMF640TestHandler(t, tmf640ACSHandler(t))
	req := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceActivationAndConfiguration/v4/service", nil)
	rec := httptest.NewRecorder()
	h.listServices(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (accountId query param required); body: %s", rec.Code, rec.Body.String())
	}
}

func TestListServicesReturnsAccountMappings(t *testing.T) {
	ctx, h, db := newTMF640TestHandler(t, tmf640ACSHandler(t))
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedTMF640Device(t, ctx, db, accountID, deviceID)

	req := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceActivationAndConfiguration/v4/service?accountId=acct-1", nil)
	rec := httptest.NewRecorder()
	h.listServices(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var svcs []tmfService
	if err := json.Unmarshal(rec.Body.Bytes(), &svcs); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(svcs) != 1 {
		t.Fatalf("got %d services, want 1", len(svcs))
	}
}

func TestGetMonitorPendingDispatch(t *testing.T) {
	ctx, h, db := newTMF640TestHandler(t, tmf640ACSHandler(t))
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedTMF640Device(t, ctx, db, accountID, deviceID)
	params := []bss.ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := h.mappings.InsertPending(ctx, "mon-pending", accountID, "SUSPEND", deviceID, params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceActivationAndConfiguration/v4/monitor/mon-pending", nil)
	req = req.WithContext(ctx)
	req.SetPathValue("id", "mon-pending")
	rec := httptest.NewRecorder()
	h.getMonitor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var mon tmfMonitor
	if err := json.Unmarshal(rec.Body.Bytes(), &mon); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if mon.State != "InProgress" {
		t.Errorf("State = %q, want InProgress", mon.State)
	}
}

func TestGetMonitorDeadLettered(t *testing.T) {
	ctx, h, db := newTMF640TestHandler(t, tmf640ACSHandler(t))
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedTMF640Device(t, ctx, db, accountID, deviceID)
	params := []bss.ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := h.mappings.InsertPending(ctx, "mon-dlq", accountID, "SUSPEND", deviceID, params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	for i := 0; i < 8; i++ {
		if err := h.mappings.MarkDispatchFailed(ctx, "mon-dlq", "boom"); err != nil {
			t.Fatalf("MarkDispatchFailed %d: %v", i, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceActivationAndConfiguration/v4/monitor/mon-dlq", nil)
	req = req.WithContext(ctx)
	req.SetPathValue("id", "mon-dlq")
	rec := httptest.NewRecorder()
	h.getMonitor(rec, req)

	var mon tmfMonitor
	if err := json.Unmarshal(rec.Body.Bytes(), &mon); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if mon.State != "InError" {
		t.Errorf("State = %q, want InError", mon.State)
	}
}

func TestGetMonitorCompleted(t *testing.T) {
	acs := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"command_key": "ck-done", "status": "SUCCESS",
		})
	})
	ctx, h, db := newTMF640TestHandler(t, acs)
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedTMF640Device(t, ctx, db, accountID, deviceID)
	params := []bss.ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := h.mappings.InsertPending(ctx, "mon-done", accountID, "SUSPEND", deviceID, params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if err := h.mappings.MarkDispatched(ctx, "mon-done", "ck-done"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceActivationAndConfiguration/v4/monitor/mon-done", nil)
	req = req.WithContext(ctx)
	req.SetPathValue("id", "mon-done")
	rec := httptest.NewRecorder()
	h.getMonitor(rec, req)

	var mon tmfMonitor
	if err := json.Unmarshal(rec.Body.Bytes(), &mon); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if mon.State != "Completed" {
		t.Errorf("State = %q, want Completed", mon.State)
	}
}

func TestGetMonitorNotFound(t *testing.T) {
	ctx, h, _ := newTMF640TestHandler(t, tmf640ACSHandler(t))
	req := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceActivationAndConfiguration/v4/monitor/no-such-order", nil)
	req = req.WithContext(ctx)
	req.SetPathValue("id", "no-such-order")
	rec := httptest.NewRecorder()
	h.getMonitor(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchServiceRequiresIdempotencyKey(t *testing.T) {
	ctx, h, db := newTMF640TestHandler(t, tmf640ACSHandler(t))
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedTMF640Device(t, ctx, db, accountID, deviceID)
	var mappingID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM account_device_mappings WHERE account_id = $1`, accountID).Scan(&mappingID); err != nil {
		t.Fatalf("read seeded mapping id: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"state": "inactive"})
	req := httptest.NewRequest(http.MethodPatch, "/tmf-api/serviceActivationAndConfiguration/v4/service/"+mappingID, bytes.NewReader(body))
	req.SetPathValue("id", mappingID)
	rec := httptest.NewRecorder()
	h.patchService(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing X-Idempotency-Key); body: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchServiceStateInactiveDispatchesSuspend(t *testing.T) {
	acs := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"command_key": "ck-suspend"})
	})
	ctx, h, db := newTMF640TestHandler(t, acs)
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedTMF640Device(t, ctx, db, accountID, deviceID)
	var mappingID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM account_device_mappings WHERE account_id = $1`, accountID).Scan(&mappingID); err != nil {
		t.Fatalf("read seeded mapping id: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"state": "inactive"})
	req := httptest.NewRequest(http.MethodPatch, "/tmf-api/serviceActivationAndConfiguration/v4/service/"+mappingID, bytes.NewReader(body))
	req.Header.Set("X-Idempotency-Key", "idem-1")
	req.SetPathValue("id", mappingID)
	rec := httptest.NewRecorder()
	h.patchService(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	var mon tmfMonitor
	if err := json.Unmarshal(rec.Body.Bytes(), &mon); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if mon.ID != "idem-1" {
		t.Errorf("Monitor.ID = %q, want idem-1 (the X-Idempotency-Key value)", mon.ID)
	}
	if mon.SourceHref == "" {
		t.Error("Monitor.SourceHref is empty, want it set to the Service's href (PATCH has the service id, unlike a bare GET /monitor)")
	}

	order, err := h.mappings.FindOrder(ctx, "idem-1")
	if err != nil {
		t.Fatalf("FindOrder: %v", err)
	}
	if order == nil || order.Action != "SUSPEND" {
		t.Fatalf("order = %+v, want Action=SUSPEND", order)
	}
}

func TestPatchServiceRejectsMixedStateAndCharacteristic(t *testing.T) {
	ctx, h, db := newTMF640TestHandler(t, tmf640ACSHandler(t))
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedTMF640Device(t, ctx, db, accountID, deviceID)
	var mappingID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM account_device_mappings WHERE account_id = $1`, accountID).Scan(&mappingID); err != nil {
		t.Fatalf("read seeded mapping id: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"state":                 "inactive",
		"serviceCharacteristic": []map[string]string{{"name": "SSID", "value": "NewName"}},
	})
	req := httptest.NewRequest(http.MethodPatch, "/tmf-api/serviceActivationAndConfiguration/v4/service/"+mappingID, bytes.NewReader(body))
	req.Header.Set("X-Idempotency-Key", "idem-mixed")
	req.SetPathValue("id", mappingID)
	rec := httptest.NewRecorder()
	h.patchService(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (mixed state + serviceCharacteristic rejected); body: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchServiceRetriedIdempotencyKeyReturnsSameMonitor(t *testing.T) {
	acs := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	ctx, h, db := newTMF640TestHandler(t, acs)
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedTMF640Device(t, ctx, db, accountID, deviceID)
	var mappingID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM account_device_mappings WHERE account_id = $1`, accountID).Scan(&mappingID); err != nil {
		t.Fatalf("read seeded mapping id: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"state": "active"})
	req1 := httptest.NewRequest(http.MethodPatch, "/tmf-api/serviceActivationAndConfiguration/v4/service/"+mappingID, bytes.NewReader(body))
	req1.Header.Set("X-Idempotency-Key", "idem-retry")
	req1.SetPathValue("id", mappingID)
	h.patchService(httptest.NewRecorder(), req1) // dispatch fails, leaves PENDING_DISPATCH

	req2 := httptest.NewRequest(http.MethodPatch, "/tmf-api/serviceActivationAndConfiguration/v4/service/"+mappingID, bytes.NewReader(body))
	req2.Header.Set("X-Idempotency-Key", "idem-retry")
	req2.SetPathValue("id", mappingID)
	rec2 := httptest.NewRecorder()
	h.patchService(rec2, req2)

	if rec2.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, want 202; body: %s", rec2.Code, rec2.Body.String())
	}
	var mon tmfMonitor
	if err := json.Unmarshal(rec2.Body.Bytes(), &mon); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}
	if mon.State != "InProgress" {
		t.Errorf("retry Monitor.State = %q, want InProgress (no second dispatch attempted)", mon.State)
	}
	_ = ctx
}
