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

// newOrderTestHandler mirrors newMappingTestHandler (mapping_test.go):
// a clean, fully migrated schema per test, wired into just enough of
// bssadapter's handler to exercise createOrder, plus an httptest ACS
// backend the handler's acsclient talks to instead of a real cmd/api.
func newOrderTestHandler(t *testing.T, acsHandler http.Handler) (context.Context, *handler, *sql.DB) {
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

func seedOrderDevice(t *testing.T, ctx context.Context, db *sql.DB, accountID, deviceID string) {
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

func TestCreateOrderSuccessWritesPendingThenDispatched(t *testing.T) {
	ctx, h, db := newOrderTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"command_key": "ck-success"})
	}))
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedOrderDevice(t, ctx, db, accountID, deviceID)

	body, _ := json.Marshal(createOrderRequest{
		ExternalOrderID: "ord-1", AccountID: accountID, Action: "SUSPEND",
	})
	req := httptest.NewRequest(http.MethodPost, "/bss/v1/orders", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.createOrder(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	var resp orderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.CommandKey != "ck-success" {
		t.Errorf("CommandKey = %q, want ck-success", resp.CommandKey)
	}

	order, err := h.mappings.FindOrder(ctx, "ord-1")
	if err != nil {
		t.Fatalf("FindOrder: %v", err)
	}
	if order.Status != bss.OrderStatusDispatched {
		t.Errorf("order.Status = %q, want %q", order.Status, bss.OrderStatusDispatched)
	}
	if order.CommandKey != "ck-success" {
		t.Errorf("order.CommandKey = %q, want ck-success", order.CommandKey)
	}
	if order.DeviceID != deviceID {
		t.Errorf("order.DeviceID = %q, want %q", order.DeviceID, deviceID)
	}
}

func TestCreateOrderTranslationRejectionWritesNoRow(t *testing.T) {
	ctx, h, db := newOrderTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("ACS must not be called when translation itself is rejected")
	}))
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedOrderDevice(t, ctx, db, accountID, deviceID)

	body, _ := json.Marshal(createOrderRequest{
		ExternalOrderID: "ord-bad-action", AccountID: accountID, Action: "NOT_A_REAL_ACTION",
	})
	req := httptest.NewRequest(http.MethodPost, "/bss/v1/orders", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.createOrder(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
	order, err := h.mappings.FindOrder(ctx, "ord-bad-action")
	if err != nil {
		t.Fatalf("FindOrder: %v", err)
	}
	if order != nil {
		t.Errorf("FindOrder = %+v, want nil: a translation-rejected order must never be written", order)
	}
}

// TestCreateOrderACSUnreachableLeavesPendingDispatch is the outbox's
// central regression test (design S3): a dispatch failure must leave a
// durable, retriable row -- not nothing, the old best-effort-log-only
// behavior.
func TestCreateOrderACSUnreachableLeavesPendingDispatch(t *testing.T) {
	ctx, h, db := newOrderTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // ACS itself errors
	}))
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedOrderDevice(t, ctx, db, accountID, deviceID)

	body, _ := json.Marshal(createOrderRequest{
		ExternalOrderID: "ord-unreachable", AccountID: accountID, Action: "SUSPEND",
	})
	req := httptest.NewRequest(http.MethodPost, "/bss/v1/orders", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.createOrder(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (ACS returned an unexpected status, not ErrACSUnreachable's own 502 case); body: %s", rec.Code, rec.Body.String())
	}

	order, err := h.mappings.FindOrder(ctx, "ord-unreachable")
	if err != nil {
		t.Fatalf("FindOrder: %v", err)
	}
	if order == nil {
		t.Fatal("FindOrder = nil after a failed dispatch, want a durable PENDING_DISPATCH row (the outbox gap this task closes)")
	}
	if order.Status != bss.OrderStatusPendingDispatch {
		t.Errorf("order.Status = %q, want %q", order.Status, bss.OrderStatusPendingDispatch)
	}
	if order.Attempts != 1 {
		t.Errorf("order.Attempts = %d, want 1", order.Attempts)
	}
	if order.DeviceID != deviceID {
		t.Errorf("order.DeviceID = %q, want %q (must survive for a later retry)", order.DeviceID, deviceID)
	}
	if len(order.Parameters) == 0 {
		t.Error("order.Parameters is empty, want the translated SUSPEND parameter write preserved for a later retry")
	}
}

// TestCreateOrderRetriedExternalOrderIDWhilePending proves the
// idempotency-check branch's new PENDING_DISPATCH/DEAD_LETTERED path: a
// retry while an order is still pending must report its own status, not
// crash trying to poll a command_key that doesn't exist yet.
func TestCreateOrderRetriedExternalOrderIDWhilePending(t *testing.T) {
	ctx, h, db := newOrderTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	const accountID, deviceID = "acct-1", "11111111-1111-1111-1111-111111111111"
	seedOrderDevice(t, ctx, db, accountID, deviceID)

	body, _ := json.Marshal(createOrderRequest{ExternalOrderID: "ord-retry", AccountID: accountID, Action: "ACTIVATE"})

	req1 := httptest.NewRequest(http.MethodPost, "/bss/v1/orders", bytes.NewReader(body))
	rec1 := httptest.NewRecorder()
	h.createOrder(rec1, req1) // fails dispatch, leaves PENDING_DISPATCH

	req2 := httptest.NewRequest(http.MethodPost, "/bss/v1/orders", bytes.NewReader(body))
	rec2 := httptest.NewRecorder()
	h.createOrder(rec2, req2)

	if rec2.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, want 202 (report current status, don't error); body: %s", rec2.Code, rec2.Body.String())
	}
	var resp orderResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}
	if resp.Status != bss.OrderStatusPendingDispatch {
		t.Errorf("retry Status = %q, want %q", resp.Status, bss.OrderStatusPendingDispatch)
	}
	if resp.CommandKey != "" {
		t.Errorf("retry CommandKey = %q, want empty (no command_key exists yet)", resp.CommandKey)
	}
}
