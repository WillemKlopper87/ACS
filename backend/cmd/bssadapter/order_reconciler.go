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
