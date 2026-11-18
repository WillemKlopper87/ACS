package bss

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"acs/internal/accounts"
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
	// ID is the order's internal key. State changes address the order by
	// it; ExternalOrderID is the integration's own identifier.
	ID string
	// Scope is the customer that owns the order's account ("" when nobody
	// does): the namespace ExternalOrderID is unique in. A worker handling
	// the order acts in it (accounts.WithCustomer).
	Scope string
	// AccountKey is the registry key of the order's account.
	AccountKey      string
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
	// An order ID is unique per customer. A caller acting for one customer
	// sees that customer's order; any other caller must not be handed one
	// of several orders that share the ID.
	scoped, scope := scopeArgs(ctx)
	if !scoped {
		var n int
		if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM bss_orders WHERE external_order_id = $1`, externalOrderID).Scan(&n); err != nil {
			return nil, fmt.Errorf("find order: %w", err)
		}
		if n > 1 {
			return nil, fmt.Errorf("%w: order %s", accounts.ErrAmbiguous, externalOrderID)
		}
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id::text, account_key::text, order_scope, external_order_id, account_id, action, device_id, parameters, command_key, status, attempts, last_error
		FROM bss_orders WHERE external_order_id = $1 AND ($2 = false OR order_scope = $3)
	`, externalOrderID, scoped, scope)
	return scanOrder(row, "find order")
}

// FindOrderByCommandKey resolves a BSS-owned ACS job back to its account.
// It deliberately returns nil for jobs not created through the BSS adapter:
// the legacy BSS status endpoint must never become an account-scoped OAuth
// client's side channel into arbitrary operator jobs.
func (r *Repository) FindOrderByCommandKey(ctx context.Context, commandKey string) (*OrderRecord, error) {
	scoped, scope := scopeArgs(ctx)
	row := r.db.QueryRowContext(ctx, `
		SELECT id::text, account_key::text, order_scope, external_order_id, account_id, action, device_id, parameters, command_key, status, attempts, last_error
		FROM bss_orders WHERE command_key = $1 AND ($2 = false OR order_scope = $3)
	`, commandKey, scoped, scope)
	return scanOrder(row, "find order by command key")
}

// OrdersForDevice returns a device's dispatched orders, most recent first
// — the BSS-recorded half of reconciliation (reconcile.go): what this
// system last told the device to become, as opposed to what the device's
// parameter cache currently reports. Only DISPATCHED orders are returned
// (a job was actually queued for them); PENDING_DISPATCH/DEAD_LETTERED
// orders never reached the CPE and would only produce false drift.
func (r *Repository) OrdersForDevice(ctx context.Context, deviceID string, limit int, accountIDs ...string) ([]OrderRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	accountKey := ""
	if id := firstAccountID(accountIDs); id != "" {
		var err error
		if accountKey, err = accounts.Resolve(ctx, r.db, id); err != nil {
			return nil, err
		}
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id::text, account_key::text, order_scope, external_order_id, account_id, action, device_id, parameters, command_key, status, attempts, COALESCE(last_error, ''), created_at
		FROM bss_orders WHERE device_id = $1 AND status = $2
		  AND ($4 = '' OR account_key = NULLIF($4, '')::uuid)
		ORDER BY created_at DESC LIMIT $3`, deviceID, OrderStatusDispatched, limit, accountKey)
	if err != nil {
		return nil, fmt.Errorf("list orders for device: %w", err)
	}
	defer rows.Close()

	var out []OrderRecord
	for rows.Next() {
		var rec OrderRecord
		var deviceIDCol, commandKey sql.NullString
		var paramsJSON []byte
		if err := rows.Scan(&rec.ID, &rec.AccountKey, &rec.Scope, &rec.ExternalOrderID, &rec.AccountID, &rec.Action, &deviceIDCol, &paramsJSON, &commandKey, &rec.Status, &rec.Attempts, &rec.LastError, &rec.CreatedAt); err != nil {
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

func firstAccountID(accountIDs []string) string {
	if len(accountIDs) == 0 {
		return ""
	}
	return accountIDs[0]
}

type orderScanner interface {
	Scan(dest ...any) error
}

func scanOrder(row orderScanner, operation string) (*OrderRecord, error) {
	var rec OrderRecord
	var deviceID, commandKey, lastError sql.NullString
	var paramsJSON []byte
	err := row.Scan(&rec.ID, &rec.AccountKey, &rec.Scope, &rec.ExternalOrderID, &rec.AccountID, &rec.Action, &deviceID, &paramsJSON, &commandKey, &rec.Status, &rec.Attempts, &lastError)
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
func (r *Repository) InsertPending(ctx context.Context, externalOrderID, accountID, action, deviceID string, params []ParameterWrite, requestDigest string) (string, error) {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal order parameters: %w", err)
	}
	accountKey, err := accounts.Ensure(ctx, r.db, accountID)
	if err != nil {
		return "", err
	}
	// order_scope is the account's customer, the namespace the order ID is
	// unique in. request_hash is the caller's digest of the request as it
	// was sent (RequestDigest); a later request with the same ID is
	// compared with it. Empty for callers that do not compare.
	var id string
	err = r.db.QueryRowContext(ctx, `
		INSERT INTO bss_orders (external_order_id, account_id, account_key, order_scope, action, device_id, parameters, status, last_attempt_at, request_hash)
		SELECT $1, $2, a.id, COALESCE(a.customer_id::text, ''), $3, $4, $5, $6, now(), $8
		  FROM bss_accounts a WHERE a.id = $7::uuid
		RETURNING id::text
	`, externalOrderID, accountID, action, deviceID, paramsJSON, OrderStatusPendingDispatch, accountKey,
		nullIfEmpty(requestDigest)).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrOrderAlreadyExists
		}
		return "", fmt.Errorf("insert pending order: %w", err)
	}
	return id, nil
}

// requestDigestVersion marks a digest of the request as the caller sent
// it. Orders recorded before such digests existed carry none, or (for a
// short while) an unmarked digest of the translated device writes, which
// is not comparable with a request: those are never compared.
const requestDigestVersion = "v1:"

// RequestDigest is the fingerprint of an order request: what was asked
// for, in the caller's own terms. Two requests with the same fingerprint
// are the same order; a different one under the same order ID is a
// different order (ErrOrderRequestChanged).
//
// It deliberately covers the request and not the device writes it was
// translated into: translation reads the device, so the same request can
// translate differently an hour later, and an identical retry must never
// look like a changed order.
func RequestDigest(fields ...any) string {
	canonical, _ := json.Marshal(fields) // map keys are sorted by encoding/json
	sum := sha256.Sum256(canonical)
	return requestDigestVersion + hex.EncodeToString(sum[:])
}

// ErrOrderRequestChanged: an order ID was sent again with a different
// request.
var ErrOrderRequestChanged = errors.New("this order ID was already used for a different request")

// SameRequest reports whether a repeated order ID carries the request the
// order was first accepted with. An order with no comparable digest on
// record, or a caller that supplies none, is taken as the same request:
// the behaviour from before digests were recorded.
func (r *Repository) SameRequest(ctx context.Context, orderID, requestDigest string) error {
	if requestDigest == "" {
		return nil
	}
	var stored sql.NullString
	if err := r.db.QueryRowContext(ctx, `SELECT request_hash FROM bss_orders WHERE id = $1::uuid`, orderID).Scan(&stored); err != nil {
		return fmt.Errorf("read order request digest: %w", err)
	}
	if !strings.HasPrefix(stored.String, requestDigestVersion) || stored.String == requestDigest {
		return nil
	}
	return ErrOrderRequestChanged
}

// MarkDispatched records that dispatch succeeded: status DISPATCHED, the
// real command_key recorded. Guarded on status = PENDING_DISPATCH (final
// review finding 5) so a DEAD_LETTERED row can never be silently
// resurrected by a stray or racing dispatch outcome.
func (r *Repository) MarkDispatched(ctx context.Context, orderID, commandKey string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE bss_orders SET status = $2, command_key = $3, last_attempt_at = now()
		WHERE id = $1::uuid AND status = $4
	`, orderID, OrderStatusDispatched, commandKey, OrderStatusPendingDispatch)
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
func (r *Repository) MarkDispatchFailed(ctx context.Context, orderID, errMsg string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE bss_orders
		SET attempts = attempts + 1, last_attempt_at = now(), last_error = $2,
		    status = CASE WHEN attempts + 1 >= $3 THEN $4 ELSE status END
		WHERE id = $1::uuid AND status = $5
	`, orderID, errMsg, maxDispatchAttempts, OrderStatusDeadLettered, OrderStatusPendingDispatch)
	if err != nil {
		return fmt.Errorf("mark order dispatch failed: %w", err)
	}
	return nil
}

// ErrOrderCeilingReached refuses a destructive order because the caller's
// customer has already placed its hourly allowance of that action.
var ErrOrderCeilingReached = errors.New("hourly ceiling for this action reached")

// OrderCeilingWindow is the period an order ceiling counts over.
const OrderCeilingWindow = time.Hour

// CountRecentOrders is how many orders of one action the context's
// customer placed within the window, whatever became of them: an order
// that was refused later still counts, so a client cannot probe for free.
// retryAfter is when the oldest of them leaves the window.
func (r *Repository) CountRecentOrders(ctx context.Context, action string, window time.Duration) (count int, retryAfter time.Duration, err error) {
	_, scope := scopeArgs(ctx)
	var oldest sql.NullTime
	err = r.db.QueryRowContext(ctx, `
		SELECT count(*), min(created_at) FROM bss_orders
		WHERE order_scope = $1 AND action = $2 AND created_at > now() - make_interval(secs => $3)
	`, scope, action, window.Seconds()).Scan(&count, &oldest)
	if err != nil {
		return 0, 0, fmt.Errorf("count recent orders: %w", err)
	}
	if oldest.Valid {
		retryAfter = time.Until(oldest.Time.Add(window))
	}
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	return count, retryAfter, nil
}

// DeviceStillAssigned reports whether an order's device is still assigned
// to the order's account. An order waiting in the outbox outlives the
// request that placed it; the assignment may not.
func (r *Repository) DeviceStillAssigned(ctx context.Context, orderID string) (bool, error) {
	var ok bool
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM bss_orders o
			JOIN account_device_mappings m ON m.account_key = o.account_key AND m.device_id = o.device_id
			WHERE o.id = $1::uuid AND m.unassigned_at IS NULL)
	`, orderID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check order device assignment: %w", err)
	}
	return ok, nil
}

// MarkDeadLettered ends a PENDING_DISPATCH order that can never be
// dispatched (the action is refused, not merely failing): no retry.
func (r *Repository) MarkDeadLettered(ctx context.Context, orderID, errMsg string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE bss_orders
		SET attempts = attempts + 1, last_attempt_at = now(), last_error = $2, status = $3
		WHERE id = $1::uuid AND status = $4
	`, orderID, errMsg, OrderStatusDeadLettered, OrderStatusPendingDispatch)
	if err != nil {
		return fmt.Errorf("mark order dead-lettered: %w", err)
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
			SELECT id
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
		WHERE o.id = c.id
		RETURNING o.id::text, o.account_key::text, o.order_scope, o.external_order_id, o.account_id, o.action, o.device_id, o.parameters, o.command_key, o.status, o.attempts, o.last_error
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
		if err := rows.Scan(&rec.ID, &rec.AccountKey, &rec.Scope, &rec.ExternalOrderID, &rec.AccountID, &rec.Action, &deviceID, &paramsJSON, &commandKey, &rec.Status, &rec.Attempts, &lastError); err != nil {
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
		SELECT id::text, account_key::text, order_scope, external_order_id, account_id, action, device_id, parameters, command_key, status, attempts, last_error
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
		if err := rows.Scan(&rec.ID, &rec.AccountKey, &rec.Scope, &rec.ExternalOrderID, &rec.AccountID, &rec.Action, &deviceID, &paramsJSON, &commandKey, &rec.Status, &rec.Attempts, &lastError); err != nil {
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
func (r *Repository) MarkOrderNotified(ctx context.Context, orderID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE bss_orders SET notified_at = now() WHERE id = $1::uuid`, orderID)
	if err != nil {
		return fmt.Errorf("mark order notified: %w", err)
	}
	return nil
}
