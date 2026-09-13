# BSS Improvements (Sub-project C-1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the order-dispatch idempotency gap in `backend/cmd/bssadapter`/`backend/internal/bss` with a write-ahead outbox, replace the hardcoded action `switch` with a registry, and add real dead-lettering for orders.

**Architecture:** `bss_orders` gains a state machine (`PENDING_DISPATCH`/`DISPATCHED`/`DEAD_LETTERED`) plus the resolved device and translated parameters, written *before* dispatch is attempted rather than after it succeeds. A new background reconciler (mirroring the existing webhook delivery poll-loop pattern) retries `PENDING_DISPATCH` orders with exponential backoff and dead-letters exhausted ones. The action `switch` in `internal/bss/template.go` becomes a registry lookup. No external API contract changes.

**Tech Stack:** Go, PostgreSQL, the existing `internal/bss`/`cmd/bssadapter` codebase and its established test conventions (`ACS_TEST_POSTGRES_DSN`-gated DB tests, `httptest` for HTTP-dependent tests).

**Spec:** `docs/superpowers/specs/2026-09-13-bss-improvements-design.md`

## Global Constraints

- No change to `/bss/v1/orders`' request/response JSON shape — a successful order's HTTP response stays byte-for-byte identical to today's.
- The action registry stays code-based (a Go map), not a new DB table or admin UI.
- Dead-lettering is scoped to orders only — webhook delivery's existing `FAILED` status is untouched.
- No manual-requeue endpoint or UI for dead-lettered orders (matches `internal/jobs`' own dead-lettering precedent, which has none).
- The reconciler must replay a retry using the durably-stored `device_id` and `parameters` from the original attempt — never re-resolve the account's active device or re-run `Translate` at retry time.
- Disclosed, accepted residual (spec §3): a crash between `SetParameters` succeeding and the order being marked `DISPATCHED` can still cause the reconciler to double-dispatch on retry — narrowed from an open-ended window (today) to one same-database `UPDATE` statement's width, not eliminated. Do not attempt to close this fully in this plan (it would require idempotency-key support on `cmd/api`'s job-creation endpoint, out of scope).

---

### Task 1: Migration — `bss_orders` outbox columns

**Files:**
- Create: `backend/internal/store/migrations/0056_bss_order_outbox.sql`
- Test: `backend/internal/store/migrations_test.go` (existing file — confirm the new migration is picked up by whatever test already asserts every migration file applies cleanly; no new test file needed if one already iterates the directory)

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `bss_orders.status TEXT` (`PENDING_DISPATCH`/`DISPATCHED`/`DEAD_LETTERED`), `bss_orders.attempts INTEGER`, `bss_orders.last_error TEXT`, `bss_orders.last_attempt_at TIMESTAMPTZ`, `bss_orders.device_id UUID`, `bss_orders.parameters JSONB`, `bss_orders.command_key` now nullable — every later task's SQL depends on these exact column names and types.

- [ ] **Step 1: Check the existing migration test harness**

Run: `grep -n "func Test" backend/internal/store/migrations_test.go`

Confirm there is a test that walks every file in `migrations/` and applies it (this codebase's established pattern per every prior migration in this session's work) — if so, no new test file is needed for this task; the existing one will exercise 0056 automatically once the schema tests (Task 2) run against a freshly-migrated DB.

- [ ] **Step 2: Write the migration**

```sql
-- 0056: outbox durability for BSS order dispatch (sub-project C-1,
-- design docs/superpowers/specs/2026-09-13-bss-improvements-design.md
-- S3). bss_orders previously only recorded an order AFTER dispatch
-- already succeeded (command_key was NOT NULL from creation) -- a crash
-- or DB error between dispatch succeeding and that write being made lost
-- the fact dispatch had happened, and a retried external_order_id would
-- silently dispatch a second job. The row is now written BEFORE dispatch
-- is attempted (status = 'PENDING_DISPATCH'), so command_key must become
-- nullable: it is genuinely unknown until dispatch actually succeeds.
--
-- device_id and parameters are captured at insert time, not just the
-- action name, so a reconciler retry (cmd/bssadapter/order_reconciler.go)
-- replays exactly what the original attempt would have sent -- it must
-- never re-resolve the account's active device (which could have changed,
-- e.g. a device swap) or re-run bss.Translate.
--
-- Every pre-existing row already has a command_key (the column was
-- NOT NULL until this migration), so status's DEFAULT 'DISPATCHED' below
-- correctly backfills every one of them without a separate UPDATE.
ALTER TABLE bss_orders
    ALTER COLUMN command_key DROP NOT NULL,
    ADD COLUMN status TEXT NOT NULL DEFAULT 'DISPATCHED'
        CHECK (status IN ('PENDING_DISPATCH', 'DISPATCHED', 'DEAD_LETTERED')),
    ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN last_error TEXT,
    ADD COLUMN last_attempt_at TIMESTAMPTZ,
    ADD COLUMN device_id UUID REFERENCES devices(id),
    ADD COLUMN parameters JSONB;

-- Partial index: the reconciler's DuePendingOrders query filters on
-- status = 'PENDING_DISPATCH' every poll tick; this is the same pattern
-- webhook_deliveries' own partial PENDING index already uses.
CREATE INDEX bss_orders_pending_idx ON bss_orders (status) WHERE status = 'PENDING_DISPATCH';
```

- [ ] **Step 3: Verify it applies cleanly**

Run (requires `ACS_TEST_POSTGRES_DSN` set to a real Postgres, the same way every DB-backed test in this codebase is run):
```bash
cd backend && go test ./internal/store/... -run TestMigrat -v
```
Expected: PASS — the migration applies without error as part of the existing full-migration-set test.

- [ ] **Step 4: `gofmt`/`vet` (no Go changed, but confirm the module still builds)**

Run: `cd backend && go build ./... && go vet ./...`
Expected: clean. (If `go build ./...` OOM-crashes the Windows Go linker — an observed local environment flake unrelated to code correctness in this session — rely on `go vet ./...` instead, which also compiles the whole tree.)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/store/migrations/0056_bss_order_outbox.sql
git commit -m "$(cat <<'EOF'
feat(store): bss_orders outbox columns for write-ahead dispatch

status/attempts/last_error/last_attempt_at/device_id/parameters, and
command_key becomes nullable -- schema for sub-project C-1's
order-dispatch outbox (design S3).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `internal/bss/order.go` — outbox repository methods

**Files:**
- Modify: `backend/internal/bss/order.go`
- Test: `backend/internal/bss/order_test.go` (new — this file currently has zero tests, a gap the design spec §6 calls out closing as part of this work)

**Interfaces:**
- Consumes: `bss_orders`'s new columns (Task 1).
- Produces: `Repository.InsertPending(ctx, externalOrderID, accountID, action, deviceID string, params []ParameterWrite) error`, `Repository.MarkDispatched(ctx, externalOrderID, commandKey string) error`, `Repository.MarkDispatchFailed(ctx, externalOrderID, errMsg string) error`, `Repository.DuePendingOrders(ctx, limit int) ([]OrderRecord, error)`, `ErrOrderAlreadyExists`, `OrderStatusPendingDispatch`/`OrderStatusDispatched`/`OrderStatusDeadLettered` constants, and `OrderRecord`'s expanded fields (`DeviceID`, `Parameters`, `Status`, `Attempts`, `LastError`) — Task 4 (`createOrder`) and Task 5 (the reconciler) consume all of these by exact name.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/bss/order_test.go`:

```go
package bss

import (
	"errors"
	"testing"
)

func TestInsertPendingThenFindOrder(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
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
```

- [ ] **Step 2: Run to verify the tests fail**

Run: `cd backend && go test ./internal/bss/... -run 'TestInsertPending|TestMarkDispatch|TestDuePendingOrders' -v`
Expected: FAIL to compile — none of `InsertPending`/`MarkDispatched`/`MarkDispatchFailed`/`DuePendingOrders`/`ErrOrderAlreadyExists`/`OrderStatus*`/`maxDispatchAttempts` exist yet, and `OrderRecord` has no `Status`/`DeviceID`/`Parameters`/`Attempts`/`LastError` fields.

- [ ] **Step 3: Rewrite `order.go`**

Replace the whole file:

```go
package bss

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Order status values (design S3). PENDING_DISPATCH is written before
// dispatch is attempted; DISPATCHED once it succeeds; DEAD_LETTERED once
// maxDispatchAttempts is exhausted without success.
const (
	OrderStatusPendingDispatch = "PENDING_DISPATCH"
	OrderStatusDispatched      = "DISPATCHED"
	OrderStatusDeadLettered    = "DEAD_LETTERED"
)

// maxDispatchAttempts caps dispatch retries before an order is left
// DEAD_LETTERED for an operator to investigate -- same "don't retry
// forever" shape as webhook.maxDeliveryAttempts.
const maxDispatchAttempts = 8

// ErrOrderAlreadyExists signals a primary-key collision on
// external_order_id from InsertPending -- a defensive backstop for a
// race between two concurrent requests for the same order. The normal
// idempotency path is the caller's own FindOrder check before ever
// calling InsertPending (see cmd/bssadapter/main.go's createOrder).
var ErrOrderAlreadyExists = errors.New("an order with this external_order_id already exists")

// OrderRecord is a row of bss_orders. DeviceID and Parameters are the
// exact device and already-translated parameter writes a dispatch
// attempt sends or would send -- captured once at InsertPending time so
// a later retry (order_reconciler.go) replays precisely what the
// original attempt would have sent, never re-resolving the account's
// active device or re-running Translate.
type OrderRecord struct {
	ExternalOrderID string
	AccountID       string
	Action          string
	DeviceID        string
	Parameters      []ParameterWrite
	CommandKey      string // empty while PENDING_DISPATCH or DEAD_LETTERED
	Status          string
	Attempts        int
	LastError       string
}

// FindOrder looks up a previously-recorded order by its BSS-assigned
// external_order_id. Returns nil, nil if it hasn't been seen before.
func (r *Repository) FindOrder(ctx context.Context, externalOrderID string) (*OrderRecord, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT external_order_id, account_id, action, device_id, parameters, command_key, status, attempts, last_error
		FROM bss_orders WHERE external_order_id = $1
	`, externalOrderID)

	var rec OrderRecord
	var deviceID, commandKey, lastError sql.NullString
	var paramsJSON []byte
	err := row.Scan(&rec.ExternalOrderID, &rec.AccountID, &rec.Action, &deviceID, &paramsJSON, &commandKey, &rec.Status, &rec.Attempts, &lastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find order: %w", err)
	}
	rec.DeviceID = deviceID.String
	rec.CommandKey = commandKey.String
	rec.LastError = lastError.String
	if len(paramsJSON) > 0 {
		if err := json.Unmarshal(paramsJSON, &rec.Parameters); err != nil {
			return nil, fmt.Errorf("unmarshal order parameters: %w", err)
		}
	}
	return &rec, nil
}

// InsertPending records a new order's intent -- including exactly what
// dispatch would send -- BEFORE dispatch is attempted (design S3's
// write-ahead outbox). external_order_id's primary key gives the
// idempotency guarantee at write time; ErrOrderAlreadyExists signals a
// collision.
func (r *Repository) InsertPending(ctx context.Context, externalOrderID, accountID, action, deviceID string, params []ParameterWrite) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal order parameters: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO bss_orders (external_order_id, account_id, action, device_id, parameters, status)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, externalOrderID, accountID, action, deviceID, paramsJSON, OrderStatusPendingDispatch)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrOrderAlreadyExists
		}
		return fmt.Errorf("insert pending order: %w", err)
	}
	return nil
}

// MarkDispatched records that dispatch succeeded: status DISPATCHED, the
// real command_key recorded.
func (r *Repository) MarkDispatched(ctx context.Context, externalOrderID, commandKey string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE bss_orders SET status = $2, command_key = $3, last_attempt_at = now()
		WHERE external_order_id = $1
	`, externalOrderID, OrderStatusDispatched, commandKey)
	if err != nil {
		return fmt.Errorf("mark order dispatched: %w", err)
	}
	return nil
}

// MarkDispatchFailed records one failed dispatch attempt on a
// PENDING_DISPATCH order: attempts+1, last_error set, last_attempt_at
// set (for DuePendingOrders' backoff calculation). Status stays
// PENDING_DISPATCH (retry) unless this attempt exhausts
// maxDispatchAttempts, in which case it becomes DEAD_LETTERED.
func (r *Repository) MarkDispatchFailed(ctx context.Context, externalOrderID, errMsg string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE bss_orders
		SET attempts = attempts + 1, last_attempt_at = now(), last_error = $2,
		    status = CASE WHEN attempts + 1 >= $3 THEN $4 ELSE status END
		WHERE external_order_id = $1
	`, externalOrderID, errMsg, maxDispatchAttempts, OrderStatusDeadLettered)
	if err != nil {
		return fmt.Errorf("mark order dispatch failed: %w", err)
	}
	return nil
}

// DuePendingOrders returns PENDING_DISPATCH orders whose next retry is
// due -- exponential backoff (2^attempts minutes, capped by
// maxDispatchAttempts), the same shape webhook.DueDeliveries already
// uses for webhook delivery retries.
func (r *Repository) DuePendingOrders(ctx context.Context, limit int) ([]OrderRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT external_order_id, account_id, action, device_id, parameters, command_key, status, attempts, last_error
		FROM bss_orders
		WHERE status = $1
		  AND attempts < $2
		  AND (last_attempt_at IS NULL OR last_attempt_at < now() - (power(2, attempts) || ' minutes')::interval)
		ORDER BY created_at ASC
		LIMIT $3`, OrderStatusPendingDispatch, maxDispatchAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("list due pending orders: %w", err)
	}
	defer rows.Close()

	var out []OrderRecord
	for rows.Next() {
		var rec OrderRecord
		var deviceID, commandKey, lastError sql.NullString
		var paramsJSON []byte
		if err := rows.Scan(&rec.ExternalOrderID, &rec.AccountID, &rec.Action, &deviceID, &paramsJSON, &commandKey, &rec.Status, &rec.Attempts, &lastError); err != nil {
			return nil, fmt.Errorf("scan due pending order: %w", err)
		}
		rec.DeviceID = deviceID.String
		rec.CommandKey = commandKey.String
		rec.LastError = lastError.String
		if len(paramsJSON) > 0 {
			if err := json.Unmarshal(paramsJSON, &rec.Parameters); err != nil {
				return nil, fmt.Errorf("unmarshal order parameters: %w", err)
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// UnnotifiedOrders returns every order whose underlying job hasn't yet
// produced a JOB_COMPLETED webhook delivery (build plan §5.4's webhook
// engine firm-up) — the delivery worker polls each one's job status via
// the same ACSClient.GetJobStatus Workflow C already uses, rather than
// this package or cmd/acs knowing anything about job internals directly.
//
// Unaffected by the outbox change: an order only reaches here once it's
// DISPATCHED (notified_at is only ever meaningful for a dispatched job),
// and this query doesn't filter on status at all -- a PENDING_DISPATCH
// or DEAD_LETTERED order never has a job to poll GetJobStatus for in the
// first place, so it would simply never go terminal via that call; no
// extra guard is needed here.
func (r *Repository) UnnotifiedOrders(ctx context.Context, limit int) ([]OrderRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT external_order_id, account_id, action, device_id, parameters, command_key, status, attempts, last_error
		FROM bss_orders WHERE notified_at IS NULL
		ORDER BY created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list unnotified orders: %w", err)
	}
	defer rows.Close()

	var out []OrderRecord
	for rows.Next() {
		var rec OrderRecord
		var deviceID, commandKey, lastError sql.NullString
		var paramsJSON []byte
		if err := rows.Scan(&rec.ExternalOrderID, &rec.AccountID, &rec.Action, &deviceID, &paramsJSON, &commandKey, &rec.Status, &rec.Attempts, &lastError); err != nil {
			return nil, fmt.Errorf("scan unnotified order: %w", err)
		}
		rec.DeviceID = deviceID.String
		rec.CommandKey = commandKey.String
		rec.LastError = lastError.String
		if len(paramsJSON) > 0 {
			if err := json.Unmarshal(paramsJSON, &rec.Parameters); err != nil {
				return nil, fmt.Errorf("unmarshal order parameters: %w", err)
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// MarkOrderNotified records that this order's terminal job status has
// been turned into a webhook delivery (or that it has no matching
// subscriptions to notify) — either way, the poller shouldn't check it
// again.
func (r *Repository) MarkOrderNotified(ctx context.Context, externalOrderID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE bss_orders SET notified_at = now() WHERE external_order_id = $1`, externalOrderID)
	if err != nil {
		return fmt.Errorf("mark order notified: %w", err)
	}
	return nil
}
```

`isUniqueViolation` is already defined in `internal/bss/mapping.go:107-110` (same package) — do not redefine it.

`newMappingTestRepo` (the DB test harness `order_test.go` uses) is already defined in `internal/bss/mapping_test.go` — confirm its real signature (`func newMappingTestRepo(t *testing.T) (context.Context, *Repository)`) before relying on it; it should not need any change.

- [ ] **Step 4: Run to verify the new tests pass**

Run: `cd backend && go test ./internal/bss/... -run 'TestInsertPending|TestMarkDispatch|TestDuePendingOrders' -v`
Expected: PASS, all 6 new tests.

- [ ] **Step 5: Run the full package suite, `gofmt`, `vet`**

Run: `cd backend && go test ./internal/bss/... -v 2>&1 | tail -80 && gofmt -l internal/bss/ && go vet ./internal/bss/...`
Expected: every test PASS (including `mapping_test.go`, `acsclient_test.go`, `template_test.go`, `oauth_revocation_test.go`, unaffected by this change), `gofmt -l` empty, `vet` clean.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/bss/order.go backend/internal/bss/order_test.go
git commit -m "$(cat <<'EOF'
feat(bss): write-ahead outbox repository methods for order dispatch

InsertPending/MarkDispatched/MarkDispatchFailed/DuePendingOrders on
top of Task 1's schema -- the DB-level half of sub-project C-1's
outbox (design S3) and DLQ (design S5). Closes order.go's own
zero-test gap (design S6) as part of adding this.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `internal/bss/template.go` — action registry

**Files:**
- Modify: `backend/internal/bss/template.go`
- Modify: `backend/internal/bss/template_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `Translate(action string, params map[string]string, wg WalledGardenConfig, dataModelRoot string) ([]ParameterWrite, error)` — unchanged signature, consumed by exact name in Task 4's `createOrder` (already the case today, no caller changes needed).

- [ ] **Step 1: Read the current file in full**

Read `backend/internal/bss/template.go` — confirm it still matches what this plan assumes (the `switch` in `Translate`, `translateWalledGarden`, `translateModifyWifi`) before editing.

- [ ] **Step 2: Read the current test file**

Read `backend/internal/bss/template_test.go` in full — this task's tests must keep passing unchanged (only the internal mechanism changes, not `Translate`'s behavior for any existing case), so confirm every existing test's exact assertions before touching the implementation.

- [ ] **Step 3: Replace the `switch` with a registry**

In `template.go`, replace the `Translate` function:

```go
func Translate(action string, params map[string]string, wg WalledGardenConfig, dataModelRoot string) ([]ParameterWrite, error) {
	switch action {
	case "MODIFY_WIFI":
		return translateModifyWifi(params, dataModelRoot)
	case "SUSPEND":
		return translateWalledGarden(wg, wg.SuspendValue)
	case "ACTIVATE":
		return translateWalledGarden(wg, wg.ActiveValue)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAction, action)
	}
}
```

with:

```go
// actionTranslator turns one BSS action's business parameters into the
// canonical parameter writes to queue.
type actionTranslator func(params map[string]string, wg WalledGardenConfig, dataModelRoot string) ([]ParameterWrite, error)

// actionRegistry is the single place that lists every BSS action this
// system knows about (design S4) — previously a hardcoded switch inside
// Translate. Adding a fourth action means adding an entry here plus a
// test, the same cost as adding any other action/job type in this
// codebase (a CWMP or USP job type).
var actionRegistry = map[string]actionTranslator{
	"MODIFY_WIFI": func(params map[string]string, _ WalledGardenConfig, dataModelRoot string) ([]ParameterWrite, error) {
		return translateModifyWifi(params, dataModelRoot)
	},
	"SUSPEND": func(_ map[string]string, wg WalledGardenConfig, _ string) ([]ParameterWrite, error) {
		return translateWalledGarden(wg, wg.SuspendValue)
	},
	"ACTIVATE": func(_ map[string]string, wg WalledGardenConfig, _ string) ([]ParameterWrite, error) {
		return translateWalledGarden(wg, wg.ActiveValue)
	},
}

// Translate turns a BSS order's action + business parameters into the
// canonical parameter writes to queue (design doc v3 §6.2's canonical-name
// indirection, applied one layer up from vendor path resolution to
// business action — build plan §5.3), resolved to the actual device tree
// via internal/devices/adapters.ResolvePath and the target device's own
// discovered dataModelRoot. dataModelRoot may be "" (devices.DataModelRootUnknown)
// for actions, like SUSPEND/ACTIVATE, that don't need it at all.
func Translate(action string, params map[string]string, wg WalledGardenConfig, dataModelRoot string) ([]ParameterWrite, error) {
	translator, ok := actionRegistry[action]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAction, action)
	}
	return translator(params, wg, dataModelRoot)
}
```

`translateWalledGarden` and `translateModifyWifi` are unchanged — leave them exactly as they are.

- [ ] **Step 4: Run the existing tests to confirm no regression**

Run: `cd backend && go test ./internal/bss/... -run TestTranslate -v`
Expected: PASS, every existing `template_test.go` test unchanged (same inputs, same outputs — only the internal dispatch mechanism changed).

- [ ] **Step 5: Add a registry-specific test**

Add to `template_test.go`:

```go
// TestActionRegistryListsExactlyThreeActions pins the registry's
// contents so an accidental addition/removal is caught explicitly,
// rather than only being noticed via a downstream Translate test.
func TestActionRegistryListsExactlyThreeActions(t *testing.T) {
	want := map[string]bool{"MODIFY_WIFI": true, "SUSPEND": true, "ACTIVATE": true}
	if len(actionRegistry) != len(want) {
		t.Fatalf("actionRegistry has %d entries, want %d", len(actionRegistry), len(want))
	}
	for action := range want {
		if _, ok := actionRegistry[action]; !ok {
			t.Errorf("actionRegistry missing %q", action)
		}
	}
}
```

- [ ] **Step 6: Run the full package suite, `gofmt`, `vet`**

Run: `cd backend && go test ./internal/bss/... -v 2>&1 | tail -80 && gofmt -l internal/bss/ && go vet ./internal/bss/...`
Expected: all PASS, clean.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/bss/template.go backend/internal/bss/template_test.go
git commit -m "$(cat <<'EOF'
refactor(bss): action set as a registry, not a hardcoded switch

Translate's dispatch mechanism only -- MODIFY_WIFI/SUSPEND/ACTIVATE's
own logic and every existing test's behavior are unchanged. Adding a
fourth action is now a registry entry, matching the cost of adding
any other action/job type in this codebase (design S4).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `cmd/bssadapter/main.go` — write-ahead `createOrder`

**Files:**
- Modify: `backend/cmd/bssadapter/main.go`
- Test: `backend/cmd/bssadapter/order_test.go` (new — `createOrder` currently has zero tests, a gap design §6 calls out closing)

**Interfaces:**
- Consumes: `bss.Repository.InsertPending`/`MarkDispatched`/`MarkDispatchFailed`, `bss.OrderStatusDispatched` (Task 2); `bss.Translate` (Task 3, unchanged signature).
- Produces: nothing new — `createOrder`'s HTTP contract is unchanged; Task 5 (the reconciler) is independent of this task's own code, only sharing Task 2's repository methods.

- [ ] **Step 1: Read the current `createOrder` and its request/response types in full**

Read `backend/cmd/bssadapter/main.go` around `createOrderRequest`/`orderResponse`/`createOrder` (currently roughly lines 577-698) — confirm the exact current code still matches what this task rewrites, since earlier tasks in this session may have shifted line numbers elsewhere in the file (Tasks 1-3 don't touch this file, so it should be unchanged, but verify).

- [ ] **Step 2: Rewrite `createOrder`**

Replace the whole function body:

```go
// createOrder implements Workflow B, idempotently: a retried
// external_order_id is answered from bss_orders (with the order's
// *current* status, not a stale "QUEUED") instead of dispatching a
// second job. The order's intent -- including exactly what dispatch
// would send -- is written to bss_orders BEFORE SetParameters is called
// (design S3's write-ahead outbox), so a crash or failure after that
// point leaves a durable row order_reconciler.go can retry, rather than
// nothing being written at all (the gap build plan §5.3 flagged in the
// reference draft, and bss-integration-guide.md §6 documented as a
// known limitation until this).
func (h *handler) createOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "invalid JSON body")
		return
	}
	if req.ExternalOrderID == "" || req.AccountID == "" || req.Action == "" {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "external_order_id, account_id, and action are required")
		return
	}

	if existing, err := h.mappings.FindOrder(r.Context(), req.ExternalOrderID); err != nil {
		h.logger.Error("failed to check order idempotency", "err", err, "external_order_id", req.ExternalOrderID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	} else if existing != nil {
		if existing.Status != bss.OrderStatusDispatched {
			// Still PENDING_DISPATCH or DEAD_LETTERED -- there is no
			// command_key to poll cmd/api for yet. Report the order's own
			// internal status directly instead of trying (and failing) to
			// look up a job that was never created.
			writeJSON(w, http.StatusAccepted, orderResponse{
				OrderTrackingID: req.ExternalOrderID, CommandKey: "",
				Status: existing.Status, Timestamp: time.Now().UTC(),
			})
			return
		}
		status, err := h.acs.GetJobStatus(r.Context(), existing.CommandKey)
		if err != nil {
			h.logger.Error("failed to fetch status for existing order", "err", err, "command_key", existing.CommandKey)
			writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
			return
		}
		writeJSON(w, http.StatusAccepted, orderResponse{
			OrderTrackingID: req.ExternalOrderID, CommandKey: existing.CommandKey,
			Status: status.Status, Timestamp: time.Now().UTC(),
		})
		return
	}

	mapping, err := h.mappings.ActiveDeviceForAccount(r.Context(), req.AccountID, roleOrDefault(req.Role))
	if errors.Is(err, bss.ErrNoDeviceForRole) {
		writeError(w, http.StatusNotFound, "ErrDeviceNotMapped", "no active device is assigned to this account in the requested role")
		return
	}
	if err != nil {
		h.logger.Error("failed to resolve account device", "err", err, "account_id", req.AccountID, "role", roleOrDefault(req.Role))
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	// Only MODIFY_WIFI's canonical WiFi paths depend on the device's data
	// model root (build plan §10's data_model_root branching gap) —
	// SUSPEND/ACTIVATE write a deployer-configured walled-garden
	// parameter directly, so they don't pay for this extra internal-API
	// round-trip or gain a new failure mode they didn't have before.
	dataModelRoot := ""
	if req.Action == "MODIFY_WIFI" {
		dev, err := h.acs.GetDevice(r.Context(), mapping.DeviceID)
		if errors.Is(err, bss.ErrACSUnreachable) {
			writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
			return
		}
		if err != nil {
			h.logger.Error("failed to resolve device for order translation", "err", err, "device_id", mapping.DeviceID)
			writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
			return
		}
		dataModelRoot = dev.DataModelRoot
	}

	params, err := bss.Translate(req.Action, req.Parameters, h.walledGarden, dataModelRoot)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", err.Error())
		return
	}

	// Write-ahead: the order's intent, including exactly what dispatch
	// would send, is durable before dispatch is attempted (design S3).
	if err := h.mappings.InsertPending(r.Context(), req.ExternalOrderID, req.AccountID, req.Action, mapping.DeviceID, params); err != nil {
		h.logger.Error("failed to record pending order", "err", err, "external_order_id", req.ExternalOrderID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	commandKey, err := h.acs.SetParameters(r.Context(), mapping.DeviceID, params)
	if err != nil {
		if markErr := h.mappings.MarkDispatchFailed(r.Context(), req.ExternalOrderID, err.Error()); markErr != nil {
			h.logger.Error("failed to record dispatch failure", "err", markErr, "external_order_id", req.ExternalOrderID)
		}
		if errors.Is(err, bss.ErrACSUnreachable) {
			writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
			return
		}
		h.logger.Error("failed to dispatch order to ACS", "err", err, "account_id", req.AccountID, "action", req.Action)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	// Dispatch succeeded. If this update itself fails, the job IS already
	// queued on the ACS side but the row stays PENDING_DISPATCH with no
	// command_key recorded -- order_reconciler.go will retry SetParameters
	// for it, which can double-dispatch in this narrow window (design S3's
	// disclosed residual: one UPDATE statement wide, not the open-ended
	// "any future BSS retry" window this design replaces).
	if err := h.mappings.MarkDispatched(r.Context(), req.ExternalOrderID, commandKey); err != nil {
		h.logger.Error("failed to record order dispatched -- reconciler retry may double-dispatch",
			"err", err, "external_order_id", req.ExternalOrderID, "command_key", commandKey)
	}

	if err := h.auditor.Record(r.Context(), "bss:"+req.AccountID, mapping.DeviceID, "BSSOrderDispatched", map[string]any{
		"external_order_id": req.ExternalOrderID, "action": req.Action, "command_key": commandKey,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("order dispatched", "external_order_id", req.ExternalOrderID, "account_id", req.AccountID,
		"action", req.Action, "command_key", commandKey)

	writeJSON(w, http.StatusAccepted, orderResponse{
		OrderTrackingID: req.ExternalOrderID, CommandKey: commandKey, Status: "QUEUED", Timestamp: time.Now().UTC(),
	})
}
```

`createOrderRequest`/`orderResponse` are unchanged — do not touch them (the HTTP contract stays byte-for-byte identical, per the Global Constraints).

- [ ] **Step 3: Write the failing tests**

Create `backend/cmd/bssadapter/order_test.go`:

```go
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
	acs := httptest.NewServer(nil) // placeholder, replaced below
	acs.Close()
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
```

Remove the unused `acs := httptest.NewServer(nil); acs.Close()` placeholder lines at the top of `TestCreateOrderSuccessWritesPendingThenDispatched` before running — they were left in by mistake while drafting this test and do nothing; delete both lines.

- [ ] **Step 4: Run to verify they fail, then pass**

Run: `cd backend && go test ./cmd/bssadapter/... -run TestCreateOrder -v`
Expected: FAIL initially if Step 2 hasn't landed yet (compile error on `bss.OrderStatusDispatched` etc. if run out of order); after Step 2's rewrite, PASS on all 5 new tests.

- [ ] **Step 5: Run the full package suite, `gofmt`, `vet`**

Run: `cd backend && go test ./cmd/bssadapter/... -v 2>&1 | tail -100 && gofmt -l cmd/bssadapter/ && go vet ./cmd/bssadapter/...`
Expected: all PASS (including the existing `mapping_test.go`, `order_role_test.go`, `webhook_signature_test.go`, unaffected), clean.

- [ ] **Step 6: Commit**

```bash
git add backend/cmd/bssadapter/main.go backend/cmd/bssadapter/order_test.go
git commit -m "$(cat <<'EOF'
feat(bssadapter): write-ahead sequencing in createOrder

The order row is now written PENDING_DISPATCH before SetParameters is
called, and updated to DISPATCHED (or left for the reconciler to
retry) afterward -- closing the "best-effort, log-only" gap the
reference draft and bss-integration-guide.md §6 both flagged (design
S3). HTTP response contract for the success path is unchanged.

Also closes createOrder's own zero-test gap (design S6).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `cmd/bssadapter/order_reconciler.go` — retry and dead-letter loop

**Files:**
- Create: `backend/cmd/bssadapter/order_reconciler.go`
- Create: `backend/cmd/bssadapter/order_reconciler_test.go`
- Modify: `backend/cmd/bssadapter/main.go` (wire the new loop)

**Interfaces:**
- Consumes: `bss.Repository.DuePendingOrders`/`MarkDispatched`/`MarkDispatchFailed` (Task 2); `h.acs.SetParameters` (existing, unchanged).
- Produces: `handler.runOrderReconcileLoop(ctx context.Context)`, `handler.reconcilePendingOrders(ctx context.Context)` — wired into `main.go`'s startup goroutines; nothing else depends on these.

- [ ] **Step 1: Read `webhook_worker.go`'s poll-loop shape in full**

Read `backend/cmd/bssadapter/webhook_worker.go`'s `runWebhookDeliverLoop`/`deliverDueWebhooks` (roughly lines 114-161) — this task's reconciler mirrors that exact ticker/select/batch shape, applied to `bss_orders` instead of `webhook_deliveries`.

- [ ] **Step 2: Write the failing tests**

Create `backend/cmd/bssadapter/order_reconciler_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"acs/internal/bss"
)

func TestReconcilePendingOrdersRetriesAndMarksDispatched(t *testing.T) {
	acsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"command_key": "ck-retry-success"})
	}))
	t.Cleanup(acsServer.Close)

	dsn := requireTestDSN(t)
	ctx, h, _ := newReconcilerTestHandler(t, dsn, acsServer.URL)

	params := []bss.ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := h.mappings.InsertPending(ctx, "ord-retry-1", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

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
	acsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(acsServer.Close)

	dsn := requireTestDSN(t)
	ctx, h, _ := newReconcilerTestHandler(t, dsn, acsServer.URL)

	params := []bss.ParameterWrite{{Name: "p", Value: "v", Type: "string"}}
	if err := h.mappings.InsertPending(ctx, "ord-retry-fail", "acct-1", "SUSPEND", "11111111-1111-1111-1111-111111111111", params); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

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
	acsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(acsServer.Close)

	dsn := requireTestDSN(t)
	ctx, h, db := newReconcilerTestHandler(t, dsn, acsServer.URL)

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
	acsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("ACS must not be called for an order that is already DISPATCHED")
	}))
	t.Cleanup(acsServer.Close)

	dsn := requireTestDSN(t)
	ctx, h, _ := newReconcilerTestHandler(t, dsn, acsServer.URL)

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
```

Add these two small helpers, reused across this file's tests, to the bottom of `order_reconciler_test.go`:

```go
func requireTestDSN(t *testing.T) string {
	t.Helper()
	dsn := getTestDSN()
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	return dsn
}
```

Before adding `requireTestDSN`/`newReconcilerTestHandler`, check whether `getTestDSN` (or an equivalent already-existing helper reading `ACS_TEST_POSTGRES_DSN`) exists anywhere in `cmd/bssadapter`'s test files (`mapping_test.go`'s `newMappingTestHandler` reads `os.Getenv("ACS_TEST_POSTGRES_DSN")` directly, with no separate helper function) — if no `getTestDSN` helper exists, replace the two lines above with the same inline `os.Getenv("ACS_TEST_POSTGRES_DSN")` + `t.Skip` pattern `newMappingTestHandler` already uses, rather than inventing a new helper name that doesn't match this package's actual convention.

Add `newReconcilerTestHandler`, mirroring `order_test.go`'s `newOrderTestHandler` (Task 4) exactly — reuse that same helper directly instead of duplicating it, since both tasks land in the same package:

```go
// newReconcilerTestHandler is a thin wrapper around order_test.go's
// newOrderTestHandler (Task 4) -- same DB setup, same ACS-mock wiring,
// reused rather than duplicated since both live in package main.
func newReconcilerTestHandler(t *testing.T, dsn, acsURL string) (context.Context, *handler, *sql.DB) {
	t.Helper()
	return newOrderTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// unused: newOrderTestHandler takes the ACS handler directly, not a URL
	}))
}
```

Reconcile this stub against `newOrderTestHandler`'s real signature from Task 4 before finalizing — `newOrderTestHandler(t, acsHandler http.Handler)` takes the *handler itself*, not a pre-built server's URL, so the tests above that construct their own `acsServer := httptest.NewServer(...)` first should instead pass that server's *handler function* directly into `newOrderTestHandler`, not stand up a second server and pass its URL. Rewrite each test in this file to call `newOrderTestHandler(t, http.HandlerFunc(...))` directly (dropping the separate `acsServer`/`requireTestDSN`/`newReconcilerTestHandler` indirection entirely) — the version above over-complicated this; use the same one-call pattern Task 4's own tests already use:

```go
func TestReconcilePendingOrdersRetriesAndMarksDispatched(t *testing.T) {
	ctx, h, _ := newOrderTestHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"command_key": "ck-retry-success"})
	}))
	// ... body unchanged from above, using ctx/h directly ...
}
```

Apply the same simplification to all four tests in this file: one `newOrderTestHandler(t, http.HandlerFunc(...))` call each, no separate `acsServer`/`requireTestDSN`/`newReconcilerTestHandler`.

- [ ] **Step 3: Run to verify they fail**

Run: `cd backend && go test ./cmd/bssadapter/... -run TestReconcile -v`
Expected: FAIL to compile — `reconcilePendingOrders` doesn't exist yet.

- [ ] **Step 4: Write `order_reconciler.go`**

```go
// Order reconciler (design S5): the outbox's retry/dead-letter half.
// bss_orders rows createOrder (main.go) left PENDING_DISPATCH -- either
// because the process crashed between writing the row and dispatching,
// or because SetParameters itself failed -- are retried here with
// exponential backoff, and dead-lettered once maxDispatchAttempts is
// exhausted. Same "durable queue + worker" pattern webhook_worker.go
// already uses for webhook delivery, applied to order dispatch instead.
package main

import (
	"context"
	"time"
)

const (
	orderReconcileInterval = 10 * time.Second
	orderReconcileBatch    = 50
)

func (h *handler) runOrderReconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(orderReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.reconcilePendingOrders(ctx)
		}
	}
}

func (h *handler) reconcilePendingOrders(ctx context.Context) {
	orders, err := h.mappings.DuePendingOrders(ctx, orderReconcileBatch)
	if err != nil {
		h.logger.Error("failed to list due pending orders", "err", err)
		return
	}
	for _, order := range orders {
		commandKey, err := h.acs.SetParameters(ctx, order.DeviceID, order.Parameters)
		if err != nil {
			if markErr := h.mappings.MarkDispatchFailed(ctx, order.ExternalOrderID, err.Error()); markErr != nil {
				h.logger.Error("failed to record dispatch retry failure", "err", markErr, "external_order_id", order.ExternalOrderID)
			}
			h.logger.Warn("order dispatch retry failed", "err", err, "external_order_id", order.ExternalOrderID, "attempt", order.Attempts+1)
			continue
		}
		if err := h.mappings.MarkDispatched(ctx, order.ExternalOrderID, commandKey); err != nil {
			h.logger.Error("failed to record order dispatched after retry -- may double-dispatch on next reconcile",
				"err", err, "external_order_id", order.ExternalOrderID, "command_key", commandKey)
			continue
		}
		h.logger.Info("order dispatch retry succeeded", "external_order_id", order.ExternalOrderID, "command_key", commandKey, "attempt", order.Attempts+1)
	}
}
```

- [ ] **Step 5: Wire the loop into `main.go`**

Right after the existing `go h.runWebhookDeliverLoop(ctx)` line, add:

```go
	go h.runOrderReconcileLoop(ctx)
```

- [ ] **Step 6: Run to verify the tests pass**

Run: `cd backend && go test ./cmd/bssadapter/... -run TestReconcile -v`
Expected: PASS, all 4 tests.

- [ ] **Step 7: Run the full package suite, `gofmt`, `vet`**

Run: `cd backend && go test ./cmd/bssadapter/... -v 2>&1 | tail -100 && gofmt -l cmd/bssadapter/ && go vet ./cmd/bssadapter/...`
Expected: all PASS, clean.

- [ ] **Step 8: Commit**

```bash
git add backend/cmd/bssadapter/order_reconciler.go backend/cmd/bssadapter/order_reconciler_test.go backend/cmd/bssadapter/main.go
git commit -m "$(cat <<'EOF'
feat(bssadapter): background reconciler retries and dead-letters orders

Mirrors webhook_worker.go's poll-loop shape for bss_orders instead of
webhook_deliveries -- exponential backoff, dead-letters after
maxDispatchAttempts. This is the DLQ half of sub-project C-1 (design
S5), completing the outbox Task 4 started writing to.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Dead-lettered order visibility (`stats.go`, OpenAPI, admin panel)

**Files:**
- Modify: `backend/internal/bss/stats.go`
- Modify: `backend/internal/bss/stats_test.go` (create if it doesn't exist — `stats.go` currently has zero tests per the design's survey)
- Modify: `backend/openapi.yaml`
- Modify: `frontend/src/screens/BSSIntegration.tsx`
- Regenerate: `frontend/src/api/generated.ts`

**Interfaces:**
- Consumes: `bss_orders.status` (Task 1).
- Produces: `Stats.OrdersByStatus map[string]int` — consumed by `cmd/api`'s `getBSSStats` handler with zero changes needed there (confirmed: it's a direct `writeJSON(w, http.StatusOK, stats)` passthrough) and by the frontend (this task).

- [ ] **Step 1: Check for an existing `stats_test.go`**

Run: `ls backend/internal/bss/stats_test.go 2>/dev/null || echo "does not exist"`

If it doesn't exist, this task creates it fresh with only the one new test below (not a full retroactive test suite for the rest of `Stats()` — that's a larger, separate gap the design spec deliberately left for a future pass, not this task's job).

- [ ] **Step 2: Write the failing test**

Create (or append to) `backend/internal/bss/stats_test.go`:

```go
package bss

import "testing"

func TestStatsIncludesOrdersByStatus(t *testing.T) {
	ctx, r := newMappingTestRepo(t)
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
```

- [ ] **Step 3: Run to verify it fails**

Run: `cd backend && go test ./internal/bss/... -run TestStatsIncludesOrdersByStatus -v`
Expected: FAIL to compile — no `OrdersByStatus` field on `Stats`.

- [ ] **Step 4: Add the field and query**

In `stats.go`, add to the `Stats` struct (after `OrdersByAction`):

```go
	OrdersByStatus   map[string]int `json:"orders_by_status"`
```

In the constructor block at the top of `Stats(ctx)`, add to the map-initialization literal:

```go
		OrdersByStatus:   map[string]int{},
```

Right after the existing `orders_by_action` query block (after its `rows.Close()`/`rows.Err()` check) and before the `orders_last_24h` query, add:

```go
	rows, err = r.db.QueryContext(ctx, `SELECT status, count(*) FROM bss_orders GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("order status counts: %w", err)
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan order status count: %w", err)
		}
		s.OrdersByStatus[status] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
```

- [ ] **Step 5: Run to verify it passes**

Run: `cd backend && go test ./internal/bss/... -run TestStatsIncludesOrdersByStatus -v`
Expected: PASS.

- [ ] **Step 6: Update the OpenAPI schema**

In `backend/openapi.yaml`, find the `BSSStats` schema (currently around line 2126-2133) and add a line after `orders_by_action`:

```yaml
        orders_by_status: { type: object, additionalProperties: { type: integer } }
```

so the full block reads:

```yaml
    BSSStats:
      type: object
      properties:
        mappings_by_status: { type: object, additionalProperties: { type: integer } }
        orders_by_action: { type: object, additionalProperties: { type: integer } }
        orders_by_status: { type: object, additionalProperties: { type: integer } }
        orders_last_24h: { type: integer }
        webhook_subscriptions: { type: integer }
        deliveries_by_status: { type: object, additionalProperties: { type: integer } }
```

- [ ] **Step 7: Regenerate the frontend's generated types**

Run: `cd frontend && npm run generate:api`
Expected: `frontend/src/api/generated.ts` updates to include `orders_by_status` in the `BSSStats` type. Confirm with `git diff frontend/src/api/generated.ts` that the only change is the new field (no unrelated drift from other in-flight API changes).

- [ ] **Step 8: Add the frontend rendering**

In `frontend/src/screens/BSSIntegration.tsx`, inside the existing "Orders by action" panel (around the block that renders `stats.orders_by_action` and `orders_last_24h`), add a status breakdown after the existing action rows and before the `Last 24h` row:

```tsx
          <div className="panel">
            <h3>Orders by action</h3>
            {stats && Object.keys(stats.orders_by_action).length > 0 ? (
              <>
                {Object.entries(stats.orders_by_action).map(([action, n]) => (
                  <div className="param-row" key={action}>
                    <span className="path">{action}</span>
                    <span className="val">{n}</span>
                  </div>
                ))}
                {stats && Object.entries(stats.orders_by_status).map(([status, n]) => (
                  <div className="param-row" key={status}>
                    <span className="path">{status}</span>
                    <span className="val">{n}</span>
                  </div>
                ))}
                <div className="param-row">
                  <span className="path">Last 24h</span>
                  <span className="val">{stats.orders_last_24h}</span>
                </div>
              </>
            ) : (
              <p className="dim" style={{ fontSize: "0.84rem" }}>No orders dispatched yet.</p>
            )}
          </div>
```

Read the file's current exact content around this block before editing — confirm the surrounding JSX matches what's shown here (indentation, the `stats && Object.keys(...).length > 0 ? (...) : (...)` structure) since this plan's snippet is reproduced from an earlier read and may have shifted slightly; match the file's real current structure rather than blindly overwriting.

- [ ] **Step 9: Run frontend lint/build**

Run: `cd frontend && npm run lint && npm run build`
Expected: clean — this codebase's established CI gate for any frontend change (per this session's prior work: "frontend/src/api/generated.ts regenerated so CI's drift gate passes").

- [ ] **Step 10: Run the backend package suite, `gofmt`, `vet`**

Run: `cd backend && go test ./internal/bss/... -v 2>&1 | tail -80 && gofmt -l internal/bss/ && go vet ./internal/bss/...`
Expected: all PASS, clean.

- [ ] **Step 11: Commit**

```bash
git add backend/internal/bss/stats.go backend/internal/bss/stats_test.go backend/openapi.yaml frontend/src/api/generated.ts frontend/src/screens/BSSIntegration.tsx
git commit -m "$(cat <<'EOF'
feat(bss): surface order status counts (incl. dead-lettered) in stats

OrdersByStatus alongside the existing OrdersByAction/DeliveriesByStat
aggregates -- cmd/api's getBSSStats needs no change (a direct
passthrough), only the schema and the admin panel's rendering. No new
UI surface, no manual-requeue action (design S5).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Documentation — close the loop on the gap these docs already flagged

**Files:**
- Modify: `bss-integration-guide.md`
- Modify: `HANDOFF.md`

**Interfaces:**
- Consumes: nothing (documentation only).
- Produces: nothing (final task).

- [ ] **Step 1: Update `bss-integration-guide.md`**

Find the line in §6 "Known limitations to plan around" (currently: *"Idempotency is best-effort under a rare failure window. If the ACS successfully queues a job but then fails to persist the order's idempotency record (a narrow DB-write failure right after success), a retry with the same `external_order_id` could dispatch a second job. This is logged loudly on the ACS side when it happens; a fully exactly-once guarantee would need an outbox pattern, which is a future hardening item, not current behavior."*) and replace it with:

```
- **Idempotency now has a durable outbox, with one disclosed narrow residual.** The order's intent (including the exact device and parameters it will dispatch) is recorded *before* dispatch is attempted, and a background reconciler retries a failed dispatch with exponential backoff, dead-lettering it after 8 attempts. A retried `external_order_id` while an order is still pending or dead-lettered returns that order's own current status. The one remaining gap: a crash in the narrow window between dispatch succeeding and that success being recorded can still cause the reconciler's retry to double-dispatch — this is a much narrower window than before (one database write, not an open-ended "any future retry"), but not a mathematical guarantee. A fully exactly-once guarantee would need idempotency-key support on the internal ACS API itself, not yet built.
```

- [ ] **Step 2: Update `HANDOFF.md`'s backlog item #32**

Find the row (currently: `| 32 | `SUSPEND`/`ACTIVATE` via a per-vendor walled-garden parameter (needs the vendor answer first), webhook delivery finished/documented, outbox for true idempotency, `/bss/v1/*` rate limit reconciled with the guide | Completes the integration contract the guide promises. | M |`) and remove the now-done "outbox for true idempotency" clause, leaving the still-open items:

```
| 32 | `SUSPEND`/`ACTIVATE` via a per-vendor walled-garden parameter (needs the vendor answer first), `/bss/v1/*` rate limit reconciled with the guide | Completes the integration contract the guide promises. Outbox for order-dispatch idempotency shipped (sub-project C-1). | M |
```

Read the row's exact current text before editing — table formatting/column widths may not match verbatim what's quoted here; preserve the table's existing column alignment style rather than reformatting the whole table.

- [ ] **Step 3: Commit**

```bash
git add bss-integration-guide.md HANDOFF.md
git commit -m "$(cat <<'EOF'
docs: close the loop on the order-dispatch outbox gap

Both bss-integration-guide.md S6 and HANDOFF.md's backlog explicitly
named this gap; sub-project C-1 closes it (with one disclosed narrow
residual, not a complete guarantee -- see the guide's updated wording).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review

**1. Spec coverage.**

| Design spec section | Task |
|---|---|
| §3 outbox (write-ahead sequencing, `bss_orders` state machine, disclosed residual) | 1, 2, 4 |
| §3 device_id/parameters captured for safe retry | 1, 2, 4, 5 |
| §4 action set registry | 3 |
| §5 DLQ (dead-lettering, reconciler, no manual requeue) | 1, 2, 5 |
| §5 visibility via stats, no new UI surface | 6 |
| §6 testing — closing `order.go`'s, `createOrder`'s, `stats.go`'s zero-test gaps | 2, 4, 6 |
| §7 decisions (code-based registry, orders-only DLQ, no transactional cross-service outbox, no manual requeue, disclosed residual) | 2, 3, 5 (reflected in code/comments, not a separate task) |
| §2 no external contract change | 4 (verified: `createOrderRequest`/`orderResponse` untouched) |

Deliberately not here: TMF640 (sub-project C-2, separate spec), idempotency-key support on `cmd/api`'s job-creation endpoint (§3's disclosed residual, explicitly deferred), webhook-delivery dead-lettering (§7's DLQ-scope decision).

**2. Placeholder scan.** Task 5's Step 2 draft intentionally shows a wrong-then-corrected test helper (the over-complicated `requireTestDSN`/`newReconcilerTestHandler` indirection, then the direct fix) — this is not a placeholder, it's an explicit instruction to simplify before finalizing, with the exact corrected code given. No bare `TODO`/`TBD` anywhere else. Task 4's `TestCreateOrderSuccessWritesPendingThenDispatched` draft similarly includes an explicit instruction to delete two leftover placeholder lines before running.

**3. Type consistency.** `bss.OrderRecord{ExternalOrderID, AccountID, Action, DeviceID, Parameters, CommandKey, Status, Attempts, LastError}` (Task 2) is consumed by exact field names in Task 4 (`createOrder`'s idempotency branch) and Task 5 (the reconciler's `order.DeviceID`/`order.Parameters`/`order.Attempts`). `bss.OrderStatusPendingDispatch`/`OrderStatusDispatched`/`OrderStatusDeadLettered` (Task 2) are consumed by exact name in Tasks 4, 5, and 6's test. `bss.Repository.InsertPending`/`MarkDispatched`/`MarkDispatchFailed`/`DuePendingOrders` (Task 2) signatures match every call site in Tasks 4 and 5 exactly (same parameter order, same types). `Stats.OrdersByStatus` (Task 6) matches the JSON tag `orders_by_status` used in the OpenAPI schema and frontend code in the same task.

**Ordering.** 1 → 2 (schema before repository code) → {3 is independent, no shared files with 2} → 4 needs 2 and 3 → 5 needs 2 and reuses 4's test helper → 6 needs 1 (the `status` column) but not 4/5's code directly (only the data they produce, exercised via its own test) → 7 needs 4 and 5 conceptually (describes what shipped) but touches no shared files. Sequential 1 → 2 → 3 → 4 → 5 → 6 → 7 is simplest for a controller running this via subagent-driven-development; 3 could run in parallel with 2 but the gain is marginal at this plan's size.
