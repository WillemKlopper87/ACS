package bss

import (
	"context"
	"fmt"
)

// Stats backs the admin panel's BSS health section — real counts from
// account_device_mappings/bss_orders/webhook_subscriptions/
// webhook_deliveries, no synthetic data.
type Stats struct {
	MappingsByStatus map[string]int `json:"mappings_by_status"`
	OrdersByAction   map[string]int `json:"orders_by_action"`
	OrdersLast24h    int            `json:"orders_last_24h"`
	WebhookSubs      int            `json:"webhook_subscriptions"`
	DeliveriesByStat map[string]int `json:"deliveries_by_status"`
}

func (r *Repository) Stats(ctx context.Context) (*Stats, error) {
	s := &Stats{
		MappingsByStatus: map[string]int{},
		OrdersByAction:   map[string]int{},
		DeliveriesByStat: map[string]int{},
	}

	rows, err := r.db.QueryContext(ctx, `SELECT status, count(*) FROM account_device_mappings GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("mapping status counts: %w", err)
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan mapping status count: %w", err)
		}
		s.MappingsByStatus[status] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = r.db.QueryContext(ctx, `SELECT action, count(*) FROM bss_orders GROUP BY action`)
	if err != nil {
		return nil, fmt.Errorf("order action counts: %w", err)
	}
	for rows.Next() {
		var action string
		var n int
		if err := rows.Scan(&action, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan order action count: %w", err)
		}
		s.OrdersByAction[action] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM bss_orders WHERE created_at > now() - interval '24 hours'`).Scan(&s.OrdersLast24h); err != nil {
		return nil, fmt.Errorf("orders last 24h: %w", err)
	}

	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM webhook_subscriptions`).Scan(&s.WebhookSubs); err != nil {
		return nil, fmt.Errorf("webhook subscription count: %w", err)
	}

	rows, err = r.db.QueryContext(ctx, `SELECT status, count(*) FROM webhook_deliveries GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("delivery status counts: %w", err)
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan delivery status count: %w", err)
		}
		s.DeliveriesByStat[status] = n
	}
	rows.Close()
	return s, rows.Err()
}
