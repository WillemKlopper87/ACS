package main

import (
	"net/http"
	"testing"
)

// TestTMF641DeleteWithoutReasonRecordsAReturn: a delete or swap item with no
// unassignReason completes with a valid default (return, upgrade); an
// unknown reason is refused before anything runs.
func TestTMF641DeleteWithoutReasonRecordsAReturn(t *testing.T) {
	ctx, h, db := newOrderTestHandler(t, acsRouterStub(t, nil, nil))
	const deviceID, newID = "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333"
	seedOrderDevice(t, ctx, db, "acct-del", deviceID)
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (id, oui_serial) VALUES ($1, $2)`, newID, newID+"-serial"); err != nil {
		t.Fatal(err)
	}
	reasonOf := func(id string) string {
		t.Helper()
		var reason string
		if err := db.QueryRowContext(ctx, `SELECT coalesce(unassign_reason, '') FROM account_device_mappings WHERE device_id = $1 ORDER BY assigned_at LIMIT 1`, id).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		return reason
	}

	if code, resp := postTMF641(t, h, tmf641OrderRequest{ExternalID: "SO-bad", AccountID: "acct-del", OrderItem: []tmf641ItemRequest{
		{Action: "delete", Role: "gateway", UnassignReason: "customer cancelled"},
	}}); code != http.StatusBadRequest {
		t.Fatalf("unknown reason = %d %v, want 400", code, resp)
	}
	if reasonOf(deviceID) != "" {
		t.Fatal("a refused order ended the assignment")
	}

	code, resp := postTMF641(t, h, tmf641OrderRequest{ExternalID: "SO-swap", AccountID: "acct-del", OrderItem: []tmf641ItemRequest{
		{Action: "delete", Role: "gateway"}, {Action: "add", Role: "gateway", OUISerial: newID + "-serial"},
	}})
	if code != http.StatusCreated || resp["state"] != "completed" {
		t.Fatalf("swap without a reason = %d %v", code, resp)
	}
	if got := reasonOf(deviceID); got != "upgrade" {
		t.Fatalf("swap reason = %q, want upgrade", got)
	}

	code, resp = postTMF641(t, h, tmf641OrderRequest{ExternalID: "SO-del", AccountID: "acct-del", OrderItem: []tmf641ItemRequest{
		{Action: "delete", Role: "gateway"},
	}})
	if code != http.StatusCreated || resp["state"] != "completed" {
		t.Fatalf("delete without a reason = %d %v", code, resp)
	}
	if got := reasonOf(newID); got != "return" {
		t.Fatalf("delete reason = %q, want return", got)
	}

	// The role is empty now: deleting it again is already done.
	code, resp = postTMF641(t, h, tmf641OrderRequest{ExternalID: "SO-del-again", AccountID: "acct-del", OrderItem: []tmf641ItemRequest{
		{Action: "delete", Role: "gateway"},
	}})
	if code != http.StatusCreated || resp["state"] != "completed" {
		t.Fatalf("delete of an empty role = %d %v", code, resp)
	}
}
