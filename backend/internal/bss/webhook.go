// Webhooks (build plan §5.4 firm-up): an outbox pattern, the same shape
// as internal/jobs's durable queue — a subscription describes where to
// deliver which event types, a delivery is one queued attempt, and a
// worker (cmd/bssadapter/webhook_worker.go) drains PENDING deliveries
// with retry/backoff, HMAC-signed. Currently the only event type is
// JOB_COMPLETED, produced by polling bss_orders' underlying job status
// via the same ACSClient.GetJobStatus Workflow C already uses — this
// package owns delivery, not detecting completion.
package bss

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"acs/internal/store"
)

var ErrSubscriptionNotFound = errors.New("webhook subscription not found")

// WebhookSubscription is a row of webhook_subscriptions. JSON tags matter
// here for the same reason as AccountDeviceMapping — the admin panel
// encodes this struct directly.
type WebhookSubscription struct {
	ID         string    `json:"id"`
	AccountID  *string   `json:"account_id"` // nil = fleet-wide
	TargetURL  string    `json:"target_url"`
	Secret     string    `json:"secret"`
	EventTypes []string  `json:"event_types"`
	CreatedAt  time.Time `json:"created_at"`
}

// WebhookDelivery is a row of webhook_deliveries.
type WebhookDelivery struct {
	ID             string
	SubscriptionID string
	TargetURL      string
	Secret         string
	EventType      string
	Payload        json.RawMessage
	Status         string
	Attempts       int
	LastAttemptAt  *time.Time
	CreatedAt      time.Time
}

type WebhookRepository struct {
	db *sql.DB
}

func NewWebhookRepository(db *sql.DB) *WebhookRepository {
	return &WebhookRepository{db: db}
}

func (r *WebhookRepository) CreateSubscription(ctx context.Context, accountID *string, targetURL, secret string, eventTypes []string) (*WebhookSubscription, error) {
	id := uuid.New().String()
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO webhook_subscriptions (id, account_id, target_url, secret, event_types)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, account_id, target_url, secret, event_types, created_at`,
		id, accountID, targetURL, secret, store.StringArray(eventTypes))
	return scanSubscription(row)
}

func (r *WebhookRepository) ListSubscriptions(ctx context.Context) ([]WebhookSubscription, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, account_id, target_url, secret, event_types, created_at FROM webhook_subscriptions ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list webhook subscriptions: %w", err)
	}
	defer rows.Close()

	var out []WebhookSubscription
	for rows.Next() {
		s, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *WebhookRepository) DeleteSubscription(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete webhook subscription: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrSubscriptionNotFound
	}
	return nil
}

// MatchingSubscriptions returns every subscription that should receive
// eventType for accountID — fleet-wide (account_id NULL) or scoped to
// this specific account, and only if eventType is in its event_types.
func (r *WebhookRepository) MatchingSubscriptions(ctx context.Context, accountID, eventType string) ([]WebhookSubscription, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, target_url, secret, event_types, created_at
		FROM webhook_subscriptions
		WHERE (account_id IS NULL OR account_id = $1) AND $2 = ANY(event_types)`,
		accountID, eventType)
	if err != nil {
		return nil, fmt.Errorf("match webhook subscriptions: %w", err)
	}
	defer rows.Close()

	var out []WebhookSubscription
	for rows.Next() {
		s, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// EnqueueDelivery queues one delivery attempt row — PENDING, zero
// attempts, picked up by the delivery worker's next poll.
func (r *WebhookRepository) EnqueueDelivery(ctx context.Context, subscriptionID, eventType string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (id, subscription_id, event_type, payload)
		VALUES ($1, $2, $3, $4)`,
		uuid.New().String(), subscriptionID, eventType, body)
	if err != nil {
		return fmt.Errorf("enqueue webhook delivery: %w", err)
	}
	return nil
}

// maxDeliveryAttempts caps retries before a delivery is left FAILED for
// good — same "don't retry forever" shape as diagnostics' max_attempts.
const maxDeliveryAttempts = 8

// DueDeliveries returns PENDING deliveries whose next retry is due —
// exponential backoff (2^attempts minutes, capped by maxDeliveryAttempts)
// so a target that's down doesn't get hammered every poll tick.
func (r *WebhookRepository) DueDeliveries(ctx context.Context, limit int) ([]WebhookDelivery, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT d.id, d.subscription_id, s.target_url, s.secret, d.event_type, d.payload, d.status, d.attempts, d.last_attempt_at, d.created_at
		FROM webhook_deliveries d
		JOIN webhook_subscriptions s ON s.id = d.subscription_id
		WHERE d.status = 'PENDING'
		  AND d.attempts < $1
		  AND (d.last_attempt_at IS NULL OR d.last_attempt_at < now() - (power(2, d.attempts) || ' minutes')::interval)
		ORDER BY d.created_at ASC
		LIMIT $2`, maxDeliveryAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("list due webhook deliveries: %w", err)
	}
	defer rows.Close()

	var out []WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		var lastAttempt sql.NullTime
		if err := rows.Scan(&d.ID, &d.SubscriptionID, &d.TargetURL, &d.Secret, &d.EventType, &d.Payload, &d.Status, &d.Attempts, &lastAttempt, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan webhook delivery: %w", err)
		}
		if lastAttempt.Valid {
			t := lastAttempt.Time
			d.LastAttemptAt = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *WebhookRepository) MarkDelivered(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE webhook_deliveries SET status = 'DELIVERED', attempts = attempts + 1, last_attempt_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("mark webhook delivery delivered: %w", err)
	}
	return nil
}

// MarkAttemptFailed records a failed delivery attempt — status stays
// PENDING (to retry) unless this attempt exhausted maxDeliveryAttempts,
// in which case it's left FAILED for an operator to investigate rather
// than retried forever.
func (r *WebhookRepository) MarkAttemptFailed(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		SET attempts = attempts + 1, last_attempt_at = now(),
		    status = CASE WHEN attempts + 1 >= $2 THEN 'FAILED' ELSE 'PENDING' END
		WHERE id = $1`, id, maxDeliveryAttempts)
	if err != nil {
		return fmt.Errorf("mark webhook delivery attempt failed: %w", err)
	}
	return nil
}

func scanSubscription(s scanner) (*WebhookSubscription, error) {
	var sub WebhookSubscription
	var accountID sql.NullString
	var eventTypes store.StringArray
	if err := s.Scan(&sub.ID, &accountID, &sub.TargetURL, &sub.Secret, &eventTypes, &sub.CreatedAt); err != nil {
		return nil, fmt.Errorf("scan webhook subscription: %w", err)
	}
	if accountID.Valid {
		sub.AccountID = &accountID.String
	}
	sub.EventTypes = []string(eventTypes)
	return &sub, nil
}
