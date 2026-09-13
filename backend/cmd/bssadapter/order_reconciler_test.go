package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"acs/internal/bss"
)

func TestReconcilePendingOrdersRetriesAndMarksDispatched(t *testing.T) {
	ctx, h, db := newOrderTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"command_key": "ck-retry-success"})
	}))
	seedOrderDevice(t, ctx, db, "acct-1", "11111111-1111-1111-1111-111111111111")

	params := []bss.ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := h.mappings.InsertPending(ctx, "ord-retry-1", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	// InsertPending now stamps last_attempt_at at insert time (final
	// review finding 1), so a genuinely fresh row isn't immediately due --
	// simulate enough time having passed since the (simulated) crash that
	// left this row PENDING_DISPATCH.
	backdateOrderLastAttempt(t, ctx, db, "ord-retry-1")

	h.reconcilePendingOrders(ctx)

	order, err := h.mappings.FindOrder(ctx, "ord-retry-1")
	if err != nil {
		t.Fatalf("FindOrder: %v", err)
	}
	if order.Status != bss.OrderStatusDispatched {
		t.Errorf("Status = %q, want %q", order.Status, bss.OrderStatusDispatched)
	}
	if order.CommandKey != "ck-retry-success" {
		t.Errorf("CommandKey = %q, want ck-retry-success", order.CommandKey)
	}
}

func TestReconcilePendingOrdersLeavesFailedRetriesPending(t *testing.T) {
	ctx, h, db := newOrderTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	seedOrderDevice(t, ctx, db, "acct-1", "11111111-1111-1111-1111-111111111111")

	params := []bss.ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := h.mappings.InsertPending(ctx, "ord-retry-fail", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	backdateOrderLastAttempt(t, ctx, db, "ord-retry-fail")

	h.reconcilePendingOrders(ctx)

	order, err := h.mappings.FindOrder(ctx, "ord-retry-fail")
	if err != nil {
		t.Fatalf("FindOrder: %v", err)
	}
	if order.Status != bss.OrderStatusPendingDispatch {
		t.Errorf("Status = %q, want still %q after one more failed attempt", order.Status, bss.OrderStatusPendingDispatch)
	}
	if order.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", order.Attempts)
	}
}

// TestReconcilePendingOrdersDeadLettersAfterMaxAttempts is the DLQ's
// end-to-end proof through the reconciler itself, not just
// MarkDispatchFailed in isolation (already covered by order_test.go in
// internal/bss).
func TestReconcilePendingOrdersDeadLettersAfterMaxAttempts(t *testing.T) {
	ctx, h, db := newOrderTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	seedOrderDevice(t, ctx, db, "acct-1", "11111111-1111-1111-1111-111111111111")

	params := []bss.ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := h.mappings.InsertPending(ctx, "ord-dlq", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	// Drive attempts to one below the cap directly (bypassing backoff,
	// which would otherwise make repeated reconcilePendingOrders calls in
	// a tight test loop withhold the row) via the same repository method
	// the reconciler itself uses, then let one final reconcile pass push
	// it over the edge and dead-letter it — proving the reconciler's own
	// call path reaches MarkDispatchFailed's dead-letter branch, not just
	// the repository method in isolation.
	for i := 0; i < 7; i++ {
		if err := h.mappings.MarkDispatchFailed(ctx, "ord-dlq", "seed"); err != nil {
			t.Fatalf("seed MarkDispatchFailed %d: %v", i, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE bss_orders SET last_attempt_at = NULL WHERE external_order_id = $1`, "ord-dlq"); err != nil {
		t.Fatalf("clear last_attempt_at to make the row immediately due: %v", err)
	}

	h.reconcilePendingOrders(ctx)

	order, err := h.mappings.FindOrder(ctx, "ord-dlq")
	if err != nil {
		t.Fatalf("FindOrder: %v", err)
	}
	if order.Status != bss.OrderStatusDeadLettered {
		t.Errorf("Status = %q, want %q", order.Status, bss.OrderStatusDeadLettered)
	}
}

func TestReconcilePendingOrdersIgnoresDispatchedOrders(t *testing.T) {
	ctx, h, db := newOrderTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("ACS must not be called for an order that is already DISPATCHED")
	}))
	seedOrderDevice(t, ctx, db, "acct-1", "11111111-1111-1111-1111-111111111111")

	params := []bss.ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := h.mappings.InsertPending(ctx, "ord-already-done", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if err := h.mappings.MarkDispatched(ctx, "ord-already-done", "ck-already"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}

	h.reconcilePendingOrders(ctx)
	// The httptest handler's t.Fatal above is the real assertion -- if
	// this reaches here without failing, SetParameters was never called.
}
