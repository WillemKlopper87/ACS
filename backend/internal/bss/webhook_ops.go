package bss

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Delivery operations for integrations (I2): see which deliveries reached
// the receiver, send a failed one again, and rotate a subscription's
// signing secret without a gap.

// ErrDeliveryNotFound: no delivery with that id on that subscription.
var ErrDeliveryNotFound = errors.New("webhook delivery not found")

// DeliveryStatus is one delivery as its subscriber may see it. The
// payload is left out: it is the event, which replay returns.
type DeliveryStatus struct {
	ID            string     `json:"id"`
	EventType     string     `json:"event_type"`
	Status        string     `json:"status"` // PENDING | DELIVERED | FAILED
	Attempts      int        `json:"attempts"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// ListDeliveries returns a subscription's deliveries, newest first,
// optionally only those with one status.
func (r *WebhookRepository) ListDeliveries(ctx context.Context, subscriptionID, status string, limit int) ([]DeliveryStatus, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, event_type, status, attempts, last_attempt_at, COALESCE(last_error, ''), created_at
		  FROM webhook_deliveries
		 WHERE subscription_id = $1 AND ($2 = '' OR status = $2)
		 ORDER BY created_at DESC LIMIT $3`, subscriptionID, status, limit)
	if err != nil {
		return nil, fmt.Errorf("list webhook deliveries: %w", err)
	}
	defer rows.Close()
	out := []DeliveryStatus{}
	for rows.Next() {
		var d DeliveryStatus
		var last sql.NullTime
		if err := rows.Scan(&d.ID, &d.EventType, &d.Status, &d.Attempts, &last, &d.LastError, &d.CreatedAt); err != nil {
			return nil, err
		}
		if last.Valid {
			t := last.Time
			d.LastAttemptAt = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Redeliver queues a delivery again from its first attempt. The receiver
// gets the same Webhook-Id, so it can deduplicate.
func (r *WebhookRepository) Redeliver(ctx context.Context, subscriptionID, deliveryID string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE webhook_deliveries SET status = 'PENDING', attempts = 0, last_attempt_at = NULL, last_error = NULL
		 WHERE id = $1 AND subscription_id = $2`, deliveryID, subscriptionID)
	if err != nil {
		return fmt.Errorf("redeliver webhook: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDeliveryNotFound
	}
	return nil
}

// RotateSecret replaces a subscription's signing secret. Until the overlap
// ends, deliveries are signed with both the new and the previous secret.
func (r *WebhookRepository) RotateSecret(ctx context.Context, subscriptionID, newSecret string, overlap time.Duration) (time.Time, error) {
	var until time.Time
	err := r.db.QueryRowContext(ctx, `
		UPDATE webhook_subscriptions
		   SET previous_secret = secret, previous_secret_expires_at = now() + $3::interval, secret = $2
		 WHERE id = $1
		RETURNING previous_secret_expires_at`, subscriptionID, newSecret, fmt.Sprintf("%d seconds", int(overlap.Seconds()))).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrSubscriptionNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("rotate webhook secret: %w", err)
	}
	return until, nil
}
