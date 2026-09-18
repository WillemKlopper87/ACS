package bss

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
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
	// CreatedAt is only populated by OrdersForDevice -- every other
	// lookup here predates needing it and scanOrder's shared column list
	// doesn't select it, so it stays the zero value elsewhere.
	CreatedAt time.Time
}

// FindOrder looks up a previously-recorded order by its BSS-assigned
// external_order_id. Returns nil, nil if it hasn't been seen before.
func (r *Repository) FindOrder(ctx context.Context, externalOrderID string) (*OrderRecord, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT external_order_id, account_id, action, device_id, parameters, command_key, status, attempts, last_error
		FROM bss_orders WHERE external_order_id = $1
	`, externalOrderID)
	return scanOrder(row, "find order")
}

// FindOrderByCommandKey resolves a BSS-owned ACS job back to its account.
// It deliberately returns nil for jobs not created through the BSS adapter:
// the legacy BSS status endpoint must never become an account-scoped OAuth
// client's side channel into arbitrary operator jobs.
func (r *Repository) FindOrderByCommandKey(ctx context.Context, commandKey string) (*OrderRecord, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT external_order_id, account_id, action, device_id, parameters, command_key, status, attempts, last_error
		FROM bss_orders WHERE command_key = $1
	`, commandKey)
	return scanOrder(row, "find order by command key")
}

// OrdersForDevice returns a device's dispatched orders, most recent first
// — the BSS-recorded half of reconciliation (reconcile.go): what this
// system last told the device to become, as opposed to what the device's
// parameter cache currently reports. Only DISPATCHED orders are returned
// (a job was actually queued for them); PENDING_DISPATCH/DEAD_LETTERED
// orders never reached the CPE and would only produce false drift.
func (r *Repository) OrdersForDevice(ctx context.Context, deviceID string, limit int) ([]OrderRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT external_order_id, account_id, action, device_id, parameters, command_key, status, attempts, COALESCE(last_error, ''), created_at
		FROM bss_orders WHERE device_id = $1 AND status = $2
		ORDER BY created_at DESC LIMIT $3`, deviceID, OrderStatusDispatched, limit)
	if err != nil {
		return nil, fmt.Errorf("list orders for device: %w", err)
	}
	defer rows.Close()

	var out []OrderRecord
	for rows.Next() {
		var rec OrderRecord
		var deviceIDCol, commandKey sql.NullString
		var paramsJSON []byte
		if err := rows.Scan(&rec.ExternalOrderID, &rec.AccountID, &rec.Action, &deviceIDCol, &paramsJSON, &commandKey, &rec.Status, &rec.Attempts, &rec.LastError, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan order for device: %w", err)
		}
		rec.DeviceID = deviceIDCol.String
		rec.CommandKey = commandKey.String
		if len(paramsJSON) > 0 {
			if err := json.Unmarshal(paramsJSON, &rec.Parameters); err != nil {
				return nil, fmt.Errorf("unmarshal order parameters: %w", err)
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

type orderScanner interface {
	Scan(dest ...any) error
}

func scanOrder(row orderScanner, operation string) (*OrderRecord, error) {
	var rec OrderRecord
	var deviceID, commandKey, lastError sql.NullString
	var paramsJSON []byte
	err := row.Scan(&rec.ExternalOrderID, &rec.AccountID, &rec.Action, &deviceID, &paramsJSON, &commandKey, &rec.Status, &rec.Attempts, &lastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
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
// collision (createOrder, main.go, answers it the same idempotent way it
// answers an ordinary retried external_order_id -- final review finding
// 6).
//
// last_attempt_at is stamped at insert time (not left NULL) as defense in
// depth for final review finding 1: it means a freshly-inserted row is
// never in the "last_attempt_at IS NULL, immediately due" state at all,
// so ClaimDuePendingOrders' first backoff-window check (2^0 = 1 minute)
// can't fire until well after any reasonable SetParameters call
// (createOrder's own, still in flight when this row was inserted) has
// resolved. The load-bearing half of that fix is ClaimDuePendingOrders'
// atomic claim itself, below.
func (r *Repository) InsertPending(ctx context.Context, externalOrderID, accountID, action, deviceID string, params []ParameterWrite) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal order parameters: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO bss_orders (external_order_id, account_id, action, device_id, parameters, status, last_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
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
// real command_key recorded. Guarded on status = PENDING_DISPATCH (final
// review finding 5) so a DEAD_LETTERED row can never be silently
// resurrected by a stray or racing dispatch outcome.
func (r *Repository) MarkDispatched(ctx context.Context, externalOrderID, commandKey string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE bss_orders SET status = $2, command_key = $3, last_attempt_at = now()
		WHERE external_order_id = $1 AND status = $4
	`, externalOrderID, OrderStatusDispatched, commandKey, OrderStatusPendingDispatch)
	if err != nil {
		return fmt.Errorf("mark order dispatched: %w", err)
	}
	return nil
}

// MarkDispatchFailed records one failed dispatch attempt on a
// PENDING_DISPATCH order: attempts+1, last_error set, last_attempt_at
// set (for ClaimDuePendingOrders' backoff calculation). Status stays
// PENDING_DISPATCH (retry) unless this attempt exhausts
// maxDispatchAttempts, in which case it becomes DEAD_LETTERED. Guarded on
// status = PENDING_DISPATCH (final review finding 5) so a DEAD_LETTERED
// row can never be silently resurrected.
func (r *Repository) MarkDispatchFailed(ctx context.Context, externalOrderID, errMsg string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE bss_orders
		SET attempts = attempts + 1, last_attempt_at = now(), last_error = $2,
		    status = CASE WHEN attempts + 1 >= $3 THEN $4 ELSE status END
		WHERE external_order_id = $1 AND status = $5
	`, externalOrderID, errMsg, maxDispatchAttempts, OrderStatusDeadLettered, OrderStatusPendingDispatch)
	if err != nil {
		return fmt.Errorf("mark order dispatch failed: %w", err)
	}
	return nil
}

// ClaimDuePendingOrders atomically selects up to limit PENDING_DISPATCH
// orders whose next retry is due -- the same exponential-backoff
// predicate (2^attempts minutes, capped by maxDispatchAttempts) a plain
// SELECT (this method's predecessor, DuePendingOrders) used to apply --
// and stamps last_attempt_at = now() on every row it returns, as part of
// the same statement, before handing them to the caller.
//
// This closes two real races final review flagged (findings 1 and 3) that
// a read-only SELECT left open:
//
//   - Finding 3: two concurrent bssadapter instances (or two overlapping
//     reconcile ticks from one instance, if a poll ever runs long) could
//     both see and dispatch the same due row -- there was no claim/lease
//     mechanism, unlike this codebase's own established idiom for exactly
//     this problem (internal/jobs/lease.go's LeaseForTypes, which claims
//     CWMP jobs the same way: SELECT ... FOR UPDATE SKIP LOCKED, then an
//     atomic status flip). FOR UPDATE SKIP LOCKED here means a row
//     another claimer already has locked is invisible to this one, so two
//     concurrent callers can never both claim it. As a result, this
//     reconciler no longer assumes at most one bssadapter instance runs
//     at a time -- the claim is safe under any number of them.
//   - Finding 1: stamping last_attempt_at at claim time, atomically with
//     selecting the row, means a row can only be claimed once per backoff
//     window -- combined with InsertPending now stamping last_attempt_at
//     at insert time too (see its comment), createOrder's own
//     synchronous InsertPending-then-SetParameters call (main.go) and a
//     concurrently-ticking reconcile pass can no longer both act on the
//     same freshly-inserted row, which is what made double-dispatch a
//     near-certainty under a slow-but-working cmd/api -- no crash needed.
//
// Implemented as a single statement (a claiming CTE feeding an UPDATE),
// rather than LeaseForTypes' explicit-transaction shape, because this
// claims a batch of rows in one pass instead of one row for one device;
// the FOR UPDATE SKIP LOCKED semantics are the same either way.
func (r *Repository) ClaimDuePendingOrders(ctx context.Context, limit int) ([]OrderRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		WITH claimed AS (
			SELECT external_order_id
			FROM bss_orders
			WHERE status = $1
			  AND attempts < $2
			  AND (last_attempt_at IS NULL OR last_attempt_at < now() - (power(2, attempts) || ' minutes')::interval)
			ORDER BY created_at ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		UPDATE bss_orders o
		SET last_attempt_at = now()
		FROM claimed c
		WHERE o.external_order_id = c.external_order_id
		RETURNING o.external_order_id, o.account_id, o.action, o.device_id, o.parameters, o.command_key, o.status, o.attempts, o.last_error
	`, OrderStatusPendingDispatch, maxDispatchAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("claim due pending orders: %w", err)
	}
	defer rows.Close()

	var out []OrderRecord
	for rows.Next() {
		var rec OrderRecord
		var deviceID, commandKey, lastError sql.NullString
		var paramsJSON []byte
		if err := rows.Scan(&rec.ExternalOrderID, &rec.AccountID, &rec.Action, &deviceID, &paramsJSON, &commandKey, &rec.Status, &rec.Attempts, &lastError); err != nil {
			return nil, fmt.Errorf("scan claimed pending order: %w", err)
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

// UnnotifiedOrders returns every DISPATCHED order whose underlying job
// hasn't yet produced a JOB_COMPLETED webhook delivery (build plan §5.4's
// webhook engine firm-up) — the delivery worker polls each one's job
// status via the same ACSClient.GetJobStatus Workflow C already uses,
// rather than this package or cmd/acs knowing anything about job
// internals directly.
//
// Filtered to status = DISPATCHED (final review finding 2): a
// PENDING_DISPATCH or DEAD_LETTERED order never has a command_key, so
// polling GetJobStatus("") for one 404s and the notify loop's per-order
// handling just continues without ever calling MarkOrderNotified -- the
// real problem isn't that such a row "never goes terminal" (an earlier,
// wrong version of this comment reasoned that meant no guard was needed);
// it's that an unfiltered, oldest-first, LIMIT-bounded query leaves that
// row parked in the result set forever, permanently occupying one of the
// batch's slots. Once enough orders dead-letter to fill the batch size on
// their own, every genuinely-dispatched order behind them is silently
// starved. DEAD_LETTERED orders remain visible instead via the admin
// panel's order-status stats (internal/bss/stats.go's OrdersByStatus) --
// there is no automatic requeue; that is an operator's decision.
func (r *Repository) UnnotifiedOrders(ctx context.Context, limit int) ([]OrderRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT external_order_id, account_id, action, device_id, parameters, command_key, status, attempts, last_error
		FROM bss_orders WHERE notified_at IS NULL AND status = $1
		ORDER BY created_at ASC LIMIT $2`, OrderStatusDispatched, limit)
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
