package bss

import (
	"errors"
	"testing"
)

// orderDeviceID is the device UUID every test in this file inserts
// orders against. bss_orders.device_id carries a foreign key to
// devices(id) (migration 0056), so each test seeds a matching devices
// row via seedDevice (already defined in mapping_test.go) before calling
// InsertPending -- otherwise InsertPending would fail on
// bss_orders_device_id_fkey rather than exercising the outbox logic
// under test.
const orderDeviceID = "11111111-1111-1111-1111-111111111111"

func TestInsertPendingThenFindOrder(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-ORDER")
	params := []ParameterWrite{{Name: "Device.WiFi.SSID.1.SSID", Value: "MyNetwork", Type: "string"}}

	if err := r.InsertPending(ctx, "ord-1", "acct-1", "MODIFY_WIFI", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	rec, err := r.FindOrder(ctx, "ord-1")
	if err != nil {
		t.Fatalf("FindOrder: %v", err)
	}
	if rec == nil {
		t.Fatal("FindOrder returned nil after InsertPending")
	}
	if rec.Status != OrderStatusPendingDispatch {
		t.Errorf("Status = %q, want %q", rec.Status, OrderStatusPendingDispatch)
	}
	if rec.CommandKey != "" {
		t.Errorf("CommandKey = %q, want empty before dispatch", rec.CommandKey)
	}
	if rec.DeviceID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("DeviceID = %q, want the inserted device id", rec.DeviceID)
	}
	if len(rec.Parameters) != 1 || rec.Parameters[0] != params[0] {
		t.Errorf("Parameters = %+v, want %+v", rec.Parameters, params)
	}
	if rec.Attempts != 0 {
		t.Errorf("Attempts = %d, want 0", rec.Attempts)
	}
}

func TestInsertPendingDuplicateExternalOrderID(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-ORDER")
	params := []ParameterWrite{{Name: "p", Value: "v", Type: "string"}}

	if err := r.InsertPending(ctx, "ord-dup", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("first InsertPending: %v", err)
	}
	err := r.InsertPending(ctx, "ord-dup", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params)
	if !errors.Is(err, ErrOrderAlreadyExists) {
		t.Fatalf("second InsertPending for the same external_order_id = %v, want ErrOrderAlreadyExists", err)
	}
}

func TestMarkDispatchedSetsCommandKeyAndStatus(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-ORDER")
	params := []ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := r.InsertPending(ctx, "ord-2", "acct-1", "ACTIVATE", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	if err := r.MarkDispatched(ctx, "ord-2", "cmd-key-123"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}

	rec, err := r.FindOrder(ctx, "ord-2")
	if err != nil {
		t.Fatalf("FindOrder: %v", err)
	}
	if rec.Status != OrderStatusDispatched {
		t.Errorf("Status = %q, want %q", rec.Status, OrderStatusDispatched)
	}
	if rec.CommandKey != "cmd-key-123" {
		t.Errorf("CommandKey = %q, want cmd-key-123", rec.CommandKey)
	}
}

func TestMarkDispatchFailedIncrementsAttemptsAndStaysPending(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-ORDER")
	params := []ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := r.InsertPending(ctx, "ord-3", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	if err := r.MarkDispatchFailed(ctx, "ord-3", "connection refused"); err != nil {
		t.Fatalf("MarkDispatchFailed: %v", err)
	}

	rec, err := r.FindOrder(ctx, "ord-3")
	if err != nil {
		t.Fatalf("FindOrder: %v", err)
	}
	if rec.Status != OrderStatusPendingDispatch {
		t.Errorf("Status after 1 failed attempt = %q, want still %q (attempts < max)", rec.Status, OrderStatusPendingDispatch)
	}
	if rec.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", rec.Attempts)
	}
	if rec.LastError != "connection refused" {
		t.Errorf("LastError = %q, want %q", rec.LastError, "connection refused")
	}
}

// TestMarkDispatchFailedDeadLettersAfterMaxAttempts proves the DLQ half
// of the design (S5): once attempts reaches maxDispatchAttempts, the
// order becomes a genuine terminal DEAD_LETTERED, not another PENDING
// retry.
func TestMarkDispatchFailedDeadLettersAfterMaxAttempts(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-ORDER")
	params := []ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := r.InsertPending(ctx, "ord-4", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	for i := 0; i < maxDispatchAttempts; i++ {
		if err := r.MarkDispatchFailed(ctx, "ord-4", "boom"); err != nil {
			t.Fatalf("MarkDispatchFailed attempt %d: %v", i+1, err)
		}
	}

	rec, err := r.FindOrder(ctx, "ord-4")
	if err != nil {
		t.Fatalf("FindOrder: %v", err)
	}
	if rec.Status != OrderStatusDeadLettered {
		t.Errorf("Status after %d failed attempts = %q, want %q", maxDispatchAttempts, rec.Status, OrderStatusDeadLettered)
	}
	if rec.Attempts != maxDispatchAttempts {
		t.Errorf("Attempts = %d, want %d", rec.Attempts, maxDispatchAttempts)
	}
}

func TestDuePendingOrdersReturnsOnlyPendingBelowMaxAttempts(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-ORDER")
	params := []ParameterWrite{{Name: "p", Value: "v", Type: "string"}}

	// A fresh pending order: due immediately (no last_attempt_at yet).
	if err := r.InsertPending(ctx, "ord-due", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending ord-due: %v", err)
	}

	// A dispatched order: must never appear.
	if err := r.InsertPending(ctx, "ord-dispatched", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending ord-dispatched: %v", err)
	}
	if err := r.MarkDispatched(ctx, "ord-dispatched", "ck-1"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}

	// A dead-lettered order: must never appear (exhausted).
	if err := r.InsertPending(ctx, "ord-dead", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending ord-dead: %v", err)
	}
	for i := 0; i < maxDispatchAttempts; i++ {
		if err := r.MarkDispatchFailed(ctx, "ord-dead", "boom"); err != nil {
			t.Fatalf("MarkDispatchFailed ord-dead attempt %d: %v", i+1, err)
		}
	}

	due, err := r.DuePendingOrders(ctx, 10)
	if err != nil {
		t.Fatalf("DuePendingOrders: %v", err)
	}
	if len(due) != 1 || due[0].ExternalOrderID != "ord-due" {
		t.Fatalf("DuePendingOrders = %+v, want exactly [ord-due]", due)
	}
}

// TestDuePendingOrdersRespectsBackoff proves a just-retried order (whose
// last_attempt_at is recent) is NOT immediately due again -- the same
// exponential-backoff shape webhook.DueDeliveries already uses.
func TestDuePendingOrdersRespectsBackoff(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-ORDER")
	params := []ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := r.InsertPending(ctx, "ord-backoff", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	// One failed attempt sets last_attempt_at = now(); backoff for
	// attempts=1 is 2^1 = 2 minutes, so it must not be due again yet.
	if err := r.MarkDispatchFailed(ctx, "ord-backoff", "boom"); err != nil {
		t.Fatalf("MarkDispatchFailed: %v", err)
	}

	due, err := r.DuePendingOrders(ctx, 10)
	if err != nil {
		t.Fatalf("DuePendingOrders: %v", err)
	}
	for _, o := range due {
		if o.ExternalOrderID == "ord-backoff" {
			t.Fatal("DuePendingOrders returned ord-backoff immediately after a failed attempt, want it withheld until its backoff window elapses")
		}
	}
}
