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
