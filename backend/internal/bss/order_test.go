package bss

import (
	"context"
	"errors"
	"sync"
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
	byKey, err := r.FindOrderByCommandKey(ctx, "cmd-key-123")
	if err != nil || byKey == nil {
		t.Fatalf("FindOrderByCommandKey: record=%+v err=%v", byKey, err)
	}
	if byKey.ExternalOrderID != "ord-2" || byKey.AccountID != "acct-1" {
		t.Errorf("FindOrderByCommandKey = %+v, want ord-2/acct-1", byKey)
	}
	missing, err := r.FindOrderByCommandKey(ctx, "not-a-bss-job")
	if err != nil || missing != nil {
		t.Errorf("FindOrderByCommandKey unknown = %+v, %v; want nil, nil", missing, err)
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

func TestClaimDuePendingOrdersReturnsOnlyPendingBelowMaxAttempts(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-ORDER")
	params := []ParameterWrite{{Name: "p", Value: "v", Type: "string"}}

	// A fresh pending order, backdated so it looks like it was inserted
	// well before its 1-minute (2^0) backoff window: due. (InsertPending
	// itself now stamps last_attempt_at = now() at insert time -- final
	// review finding 1 -- so a genuinely fresh row is deliberately NOT
	// immediately due; TestClaimDuePendingOrdersDoesNotClaimFreshlyInserted
	// below proves that half directly.)
	if err := r.InsertPending(ctx, "ord-due", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending ord-due: %v", err)
	}
	backdateLastAttempt(t, ctx, r, "ord-due")

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

	due, err := r.ClaimDuePendingOrders(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimDuePendingOrders: %v", err)
	}
	if len(due) != 1 || due[0].ExternalOrderID != "ord-due" {
		t.Fatalf("ClaimDuePendingOrders = %+v, want exactly [ord-due]", due)
	}
}

// TestClaimDuePendingOrdersRespectsBackoff proves a just-retried order
// (whose last_attempt_at is recent) is NOT immediately due again -- the
// same exponential-backoff shape webhook.DueDeliveries already uses.
func TestClaimDuePendingOrdersRespectsBackoff(t *testing.T) {
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

	due, err := r.ClaimDuePendingOrders(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimDuePendingOrders: %v", err)
	}
	for _, o := range due {
		if o.ExternalOrderID == "ord-backoff" {
			t.Fatal("ClaimDuePendingOrders returned ord-backoff immediately after a failed attempt, want it withheld until its backoff window elapses")
		}
	}
}

// backdateLastAttempt pushes a row's last_attempt_at far enough into the
// past that it reads as due under any attempts count up to
// maxDispatchAttempts (2^attempts minutes, so comfortably more than
// 2^maxDispatchAttempts minutes back) -- the direct-SQL equivalent of "an
// operator waited long enough" for tests that need a due row without
// waiting on a real clock.
func backdateLastAttempt(t *testing.T, ctx context.Context, r *Repository, externalOrderID string) {
	t.Helper()
	if _, err := r.db.ExecContext(ctx,
		`UPDATE bss_orders SET last_attempt_at = now() - interval '1000 minutes' WHERE external_order_id = $1`,
		externalOrderID); err != nil {
		t.Fatalf("backdate last_attempt_at for %s: %v", externalOrderID, err)
	}
}

// TestClaimDuePendingOrdersDoesNotClaimFreshlyInserted is the regression
// test for final review finding 1: a just-InsertPending'd order (zero
// attempts, last_attempt_at stamped at insert time rather than left NULL)
// must NOT be claimable again the instant it's inserted. Before this fix,
// a fresh row's last_attempt_at was NULL, which DuePendingOrders' backoff
// predicate treated as "immediately due" -- so for the entire duration of
// createOrder's own in-flight SetParameters call, the reconciler could
// claim and dispatch the same order a second time. Two back-to-back
// claims immediately after InsertPending, within the same tick, prove
// neither one sees the row.
func TestClaimDuePendingOrdersDoesNotClaimFreshlyInserted(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-ORDER")
	params := []ParameterWrite{{Name: "p", Value: "v", Type: "string"}}

	if err := r.InsertPending(ctx, "ord-fresh", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	for i := 0; i < 2; i++ {
		due, err := r.ClaimDuePendingOrders(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimDuePendingOrders call %d: %v", i+1, err)
		}
		for _, o := range due {
			if o.ExternalOrderID == "ord-fresh" {
				t.Fatalf("ClaimDuePendingOrders call %d claimed ord-fresh immediately after InsertPending -- want it withheld for its full backoff window (this is finding 1's double-dispatch race)", i+1)
			}
		}
	}
}

// TestClaimDuePendingOrdersSkipLockedExcludesConcurrentClaimant proves
// the FOR UPDATE SKIP LOCKED half of the claim (final review finding 3):
// two concurrent claimants racing for the same due row must never both
// get it. Both goroutines share one Repository/*sql.DB pool -- exactly
// how two goroutines inside one bssadapter process would (and, since a
// connection pool is just a set of otherwise-independent Postgres
// backend connections, the same row-lock visibility a second process
// with its own pool would see: ClaimDuePendingOrders is one
// unwrapped statement, so each call is its own implicit transaction on
// whichever connection the pool hands it).
func TestClaimDuePendingOrdersSkipLockedExcludesConcurrentClaimant(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-ORDER")
	params := []ParameterWrite{{Name: "p", Value: "v", Type: "string"}}

	if err := r.InsertPending(ctx, "ord-race", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	backdateLastAttempt(t, ctx, r, "ord-race")

	var wg sync.WaitGroup
	var claims [2][]OrderRecord
	var errs [2]error
	wg.Add(2)
	go func() { defer wg.Done(); claims[0], errs[0] = r.ClaimDuePendingOrders(ctx, 10) }()
	go func() { defer wg.Done(); claims[1], errs[1] = r.ClaimDuePendingOrders(ctx, 10) }()
	wg.Wait()

	if errs[0] != nil {
		t.Fatalf("claimant 1: %v", errs[0])
	}
	if errs[1] != nil {
		t.Fatalf("claimant 2: %v", errs[1])
	}

	got := 0
	for _, claim := range claims {
		for _, o := range claim {
			if o.ExternalOrderID == "ord-race" {
				got++
			}
		}
	}
	if got != 1 {
		t.Fatalf("ord-race was claimed by %d concurrent callers, want exactly 1 (FOR UPDATE SKIP LOCKED must make it invisible to the loser)", got)
	}
}

// TestUnnotifiedOrdersOnlyReturnsDispatched is the regression test for
// final review finding 2: a DEAD_LETTERED order (empty command_key) used
// to never leave UnnotifiedOrders' oldest-first, LIMIT-bounded result
// set -- GetJobStatus("") 404s forever, so notifyTerminalOrders'
// (webhook_worker.go) per-order handling just `continue`s without ever
// calling MarkOrderNotified, and the row stays parked in the batch on
// every subsequent poll. Enough dead-lettered orders eventually fill the
// whole batch and permanently starve every genuinely-dispatched order
// behind them. Proves DEAD_LETTERED and PENDING_DISPATCH orders never
// appear, and a DISPATCHED order with notified_at still NULL does.
func TestUnnotifiedOrdersOnlyReturnsDispatched(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
	seedDevice(t, ctx, r, orderDeviceID, "S-ORDER")
	params := []ParameterWrite{{Name: "p", Value: "v", Type: "string"}}

	// PENDING_DISPATCH: no command_key to poll GetJobStatus for yet.
	if err := r.InsertPending(ctx, "ord-pending", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending ord-pending: %v", err)
	}

	// DEAD_LETTERED: empty command_key forever -- the row this finding is
	// about.
	if err := r.InsertPending(ctx, "ord-dead", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending ord-dead: %v", err)
	}
	for i := 0; i < maxDispatchAttempts; i++ {
		if err := r.MarkDispatchFailed(ctx, "ord-dead", "boom"); err != nil {
			t.Fatalf("MarkDispatchFailed ord-dead attempt %d: %v", i+1, err)
		}
	}

	// DISPATCHED, not yet notified: the one row that must appear.
	if err := r.InsertPending(ctx, "ord-dispatched", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending ord-dispatched: %v", err)
	}
	if err := r.MarkDispatched(ctx, "ord-dispatched", "ck-1"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}

	unnotified, err := r.UnnotifiedOrders(ctx, 50)
	if err != nil {
		t.Fatalf("UnnotifiedOrders: %v", err)
	}
	if len(unnotified) != 1 || unnotified[0].ExternalOrderID != "ord-dispatched" {
		t.Fatalf("UnnotifiedOrders = %+v, want exactly [ord-dispatched]", unnotified)
	}
}
