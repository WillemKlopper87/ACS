package main

import (
	"encoding/json"
	"testing"

	"acs/internal/bss"
)

func TestTMF641SwapPair(t *testing.T) {
	tests := []struct {
		name  string
		items []tmf641ItemRequest
		want  bool
	}{
		{"delete then add same role", []tmf641ItemRequest{{Action: "delete", Role: "gateway"}, {Action: "add", Role: "gateway", OUISerial: "001122-S2"}}, true},
		{"default role", []tmf641ItemRequest{{Action: "delete"}, {Action: "add", OUISerial: "001122-S2"}}, true},
		{"add then delete", []tmf641ItemRequest{{Action: "add", Role: "gateway", OUISerial: "001122-S2"}, {Action: "delete", Role: "gateway"}}, false},
		{"different roles", []tmf641ItemRequest{{Action: "delete", Role: "gateway"}, {Action: "add", Role: "ont", OUISerial: "001122-S2"}}, false},
		{"three items", []tmf641ItemRequest{{Action: "delete", Role: "gateway"}, {Action: "add", Role: "gateway", OUISerial: "001122-S2"}, {Action: "noChange"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got := tmf641SwapPair(tt.items)
			if got != tt.want {
				t.Fatalf("tmf641SwapPair() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTMF641RecoveryReplaysExpiredAddLeaseIdempotently(t *testing.T) {
	ctx, h, db := newOrderTestHandler(t, acsRouterStub(t, nil, nil))
	const accountID, deviceID = "acct-recovery", "11111111-1111-1111-1111-111111111111"
	seedOrderDevice(t, ctx, db, accountID, deviceID)
	req := tmf641OrderRequest{ExternalID: "recovery-add", AccountID: accountID,
		OrderItem: []tmf641ItemRequest{{Action: "add", Role: "gateway", OUISerial: deviceID + "-serial"}}}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	order, err := h.mappings.CreateServiceOrder(ctx, req.ExternalID, accountID, raw,
		[]bss.ServiceOrderItem{{Seq: 0, Action: "add", Role: "gateway", Status: "PENDING"}})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a process dying after it leased the item and after the mapping
	// insert committed, but before it recorded the item as complete.
	if _, err := db.ExecContext(ctx, `UPDATE service_orders SET created_at = now() - interval '1 minute' WHERE id = $1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE service_order_items SET status='RUNNING', claimed_at=now() - interval '11 minutes' WHERE order_id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}

	h.reconcileTMF641Orders(ctx)
	items, err := h.mappings.ServiceOrderItems(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != "COMPLETED" {
		t.Fatalf("recovered items = %+v, want completed", items)
	}
	var active int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM account_device_mappings WHERE account_id=$1 AND device_id=$2 AND unassigned_at IS NULL`, accountID, deviceID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active mappings = %d, want exactly one", active)
	}
}
