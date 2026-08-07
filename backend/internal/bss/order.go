package bss

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// OrderRecord is a row of bss_orders — the idempotency ledger the
// reference internal_bss_adapter.go draft was missing (build plan §5.3:
// without it, a BSS retry on timeout double-dispatches the order).
type OrderRecord struct {
	ExternalOrderID string
	AccountID       string
	Action          string
	CommandKey      string
}

// FindOrder looks up a previously-recorded order by its BSS-assigned
// external_order_id. Returns nil, nil if it hasn't been seen before.
func (r *Repository) FindOrder(ctx context.Context, externalOrderID string) (*OrderRecord, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT external_order_id, account_id, action, command_key
		FROM bss_orders WHERE external_order_id = $1
	`, externalOrderID)

	var rec OrderRecord
	err := row.Scan(&rec.ExternalOrderID, &rec.AccountID, &rec.Action, &rec.CommandKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find order: %w", err)
	}
	return &rec, nil
}

// RecordOrder saves the mapping from a BSS order to the ACS job it
// produced, so a retried submission of the same external_order_id can be
// answered from this table instead of dispatching a second job.
func (r *Repository) RecordOrder(ctx context.Context, rec OrderRecord) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO bss_orders (external_order_id, account_id, action, command_key)
		VALUES ($1, $2, $3, $4)
	`, rec.ExternalOrderID, rec.AccountID, rec.Action, rec.CommandKey)
	if err != nil {
		return fmt.Errorf("record order: %w", err)
	}
	return nil
}

// UnnotifiedOrders returns every order whose underlying job hasn't yet
// produced a JOB_COMPLETED webhook delivery (build plan §5.4's webhook
// engine firm-up) — the delivery worker polls each one's job status via
// the same ACSClient.GetJobStatus Workflow C already uses, rather than
// this package or cmd/acs knowing anything about job internals directly.
func (r *Repository) UnnotifiedOrders(ctx context.Context, limit int) ([]OrderRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT external_order_id, account_id, action, command_key
		FROM bss_orders WHERE notified_at IS NULL
		ORDER BY created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list unnotified orders: %w", err)
	}
	defer rows.Close()

	var out []OrderRecord
	for rows.Next() {
		var rec OrderRecord
		if err := rows.Scan(&rec.ExternalOrderID, &rec.AccountID, &rec.Action, &rec.CommandKey); err != nil {
			return nil, fmt.Errorf("scan unnotified order: %w", err)
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
