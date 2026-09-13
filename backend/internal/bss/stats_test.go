package bss

import "testing"

func TestStatsIncludesOrdersByStatus(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-STATS")
	params := []ParameterWrite{{Name: "p", Value: "v", Type: "string"}}

	if err := r.InsertPending(ctx, "ord-pending", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if err := r.InsertPending(ctx, "ord-dispatched", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if err := r.MarkDispatched(ctx, "ord-dispatched", "ck-1"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}

	stats, err := r.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.OrdersByStatus[OrderStatusPendingDispatch] != 1 {
		t.Errorf("OrdersByStatus[%s] = %d, want 1", OrderStatusPendingDispatch, stats.OrdersByStatus[OrderStatusPendingDispatch])
	}
	if stats.OrdersByStatus[OrderStatusDispatched] != 1 {
		t.Errorf("OrdersByStatus[%s] = %d, want 1", OrderStatusDispatched, stats.OrdersByStatus[OrderStatusDispatched])
	}
}
