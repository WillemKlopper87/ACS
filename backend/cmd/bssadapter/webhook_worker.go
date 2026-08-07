// Webhook engine (build plan §5.4 firm-up): two independent poll loops,
// the same "durable queue + worker" pattern already proven for CWMP jobs
// (internal/jobs) in Phase 2, applied here to outbound HTTP instead of
// outbound CWMP RPCs.
//
//  1. notifyLoop watches bss_orders for jobs that have gone terminal and
//     turns each one into a webhook_deliveries row per matching
//     subscription — detection goes through the same GetJobStatus call
//     Workflow C already uses, so this worker needs no direct access to
//     the jobs table (bssadapter never has, by design — build plan §5.1).
//  2. deliverLoop drains due (PENDING, backoff-elapsed) deliveries and
//     POSTs them with an HMAC-SHA256 signature, same as any standard
//     webhook contract.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"acs/internal/bss"
)

const (
	webhookNotifyInterval  = 10 * time.Second
	webhookDeliverInterval = 10 * time.Second
	webhookBatchSize       = 50
	webhookHTTPTimeout     = 10 * time.Second
)

// jobCompletedPayload is the JOB_COMPLETED event body — the guide's
// Workflow C shape, pushed instead of polled.
type jobCompletedPayload struct {
	EventType       string  `json:"event_type"`
	ExternalOrderID string  `json:"external_order_id"`
	AccountID       string  `json:"account_id"`
	Action          string  `json:"action"`
	CommandKey      string  `json:"command_key"`
	Status          string  `json:"status"`
	CompletedAt     *string `json:"completed_at,omitempty"`
	FaultCode       *string `json:"fault_code,omitempty"`
	FaultString     *string `json:"fault_string,omitempty"`
}

func isTerminalJobStatus(status string) bool {
	switch status {
	case "SUCCESS", "FAILED", "TIMEOUT":
		return true
	default:
		return false
	}
}

func (h *handler) runWebhookNotifyLoop(ctx context.Context) {
	ticker := time.NewTicker(webhookNotifyInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.notifyTerminalOrders(ctx)
		}
	}
}

func (h *handler) notifyTerminalOrders(ctx context.Context) {
	orders, err := h.mappings.UnnotifiedOrders(ctx, webhookBatchSize)
	if err != nil {
		h.logger.Error("failed to list unnotified bss orders", "err", err)
		return
	}
	for _, order := range orders {
		status, err := h.acs.GetJobStatus(ctx, order.CommandKey)
		if err != nil {
			h.logger.Warn("failed to check order job status for webhook notify", "err", err, "external_order_id", order.ExternalOrderID)
			continue
		}
		if !isTerminalJobStatus(status.Status) {
			continue // still running — check again next tick
		}

		subs, err := h.webhooks.MatchingSubscriptions(ctx, order.AccountID, "JOB_COMPLETED")
		if err != nil {
			h.logger.Error("failed to match webhook subscriptions", "err", err, "account_id", order.AccountID)
			continue
		}
		payload := jobCompletedPayload{
			EventType: "JOB_COMPLETED", ExternalOrderID: order.ExternalOrderID, AccountID: order.AccountID,
			Action: order.Action, CommandKey: order.CommandKey, Status: status.Status,
			CompletedAt: status.CompletedAt, FaultCode: status.FaultCode, FaultString: status.FaultString,
		}
		for _, sub := range subs {
			if err := h.webhooks.EnqueueDelivery(ctx, sub.ID, "JOB_COMPLETED", payload); err != nil {
				h.logger.Error("failed to enqueue webhook delivery", "err", err, "subscription_id", sub.ID)
			}
		}
		if err := h.mappings.MarkOrderNotified(ctx, order.ExternalOrderID); err != nil {
			h.logger.Error("failed to mark order notified", "err", err, "external_order_id", order.ExternalOrderID)
			continue
		}
		h.logger.Info("order job completed, webhook deliveries enqueued", "external_order_id", order.ExternalOrderID, "status", status.Status, "subscriptions", len(subs))
	}
}

func (h *handler) runWebhookDeliverLoop(ctx context.Context) {
	ticker := time.NewTicker(webhookDeliverInterval)
	defer ticker.Stop()
	client := &http.Client{Timeout: webhookHTTPTimeout}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.deliverDueWebhooks(ctx, client)
		}
	}
}

func (h *handler) deliverDueWebhooks(ctx context.Context, client *http.Client) {
	deliveries, err := h.webhooks.DueDeliveries(ctx, webhookBatchSize)
	if err != nil {
		h.logger.Error("failed to list due webhook deliveries", "err", err)
		return
	}
	for _, d := range deliveries {
		if h.sendWebhookDelivery(ctx, client, d) {
			if err := h.webhooks.MarkDelivered(ctx, d.ID); err != nil {
				h.logger.Error("failed to mark webhook delivery delivered", "err", err, "delivery_id", d.ID)
			}
		} else if err := h.webhooks.MarkAttemptFailed(ctx, d.ID); err != nil {
			h.logger.Error("failed to mark webhook delivery attempt failed", "err", err, "delivery_id", d.ID)
		}
	}
}

// sendWebhookDelivery POSTs one delivery, signed the way most webhook
// contracts expect: X-Webhook-Signature is hex(HMAC-SHA256(secret, body)),
// letting the receiver verify the payload actually came from here and
// wasn't tampered with in transit.
func (h *handler) sendWebhookDelivery(ctx context.Context, client *http.Client, d bss.WebhookDelivery) bool {
	mac := hmac.New(sha256.New, []byte(d.Secret))
	mac.Write(d.Payload)
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TargetURL, bytes.NewReader(d.Payload))
	if err != nil {
		h.logger.Error("failed to build webhook delivery request", "err", err, "delivery_id", d.ID)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", signature)
	req.Header.Set("X-Webhook-Event", d.EventType)

	resp, err := client.Do(req)
	if err != nil {
		h.logger.Warn("webhook delivery request failed", "err", err, "delivery_id", d.ID, "target_url", d.TargetURL, "attempt", d.Attempts+1)
		return false
	}
	defer resp.Body.Close()

	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !ok {
		h.logger.Warn("webhook delivery rejected", "delivery_id", d.ID, "target_url", d.TargetURL, "status", resp.StatusCode, "attempt", d.Attempts+1)
	}
	return ok
}

// --- subscription management REST endpoints ---

type createWebhookSubscriptionRequest struct {
	AccountID  *string  `json:"account_id,omitempty"`
	TargetURL  string   `json:"target_url"`
	Secret     string   `json:"secret"`
	EventTypes []string `json:"event_types"`
}

type webhookSubscriptionResponse struct {
	ID         string   `json:"id"`
	AccountID  *string  `json:"account_id,omitempty"`
	TargetURL  string   `json:"target_url"`
	EventTypes []string `json:"event_types"`
	CreatedAt  string   `json:"created_at"`
}

func toWebhookSubscriptionResponse(s *bss.WebhookSubscription) webhookSubscriptionResponse {
	return webhookSubscriptionResponse{
		ID: s.ID, AccountID: s.AccountID, TargetURL: s.TargetURL,
		EventTypes: s.EventTypes, CreatedAt: s.CreatedAt.Format(time.RFC3339),
	}
}

func (h *handler) createWebhookSubscription(w http.ResponseWriter, r *http.Request) {
	var req createWebhookSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "invalid JSON body")
		return
	}
	if req.TargetURL == "" || req.Secret == "" || len(req.EventTypes) == 0 {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "target_url, secret, and at least one event_type are required")
		return
	}

	created, err := h.webhooks.CreateSubscription(r.Context(), req.AccountID, req.TargetURL, req.Secret, req.EventTypes)
	if err != nil {
		h.logger.Error("failed to create webhook subscription", "err", err)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, toWebhookSubscriptionResponse(created))
}

func (h *handler) listWebhookSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs, err := h.webhooks.ListSubscriptions(r.Context())
	if err != nil {
		h.logger.Error("failed to list webhook subscriptions", "err", err)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	items := make([]webhookSubscriptionResponse, 0, len(subs))
	for _, s := range subs {
		items = append(items, toWebhookSubscriptionResponse(&s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) deleteWebhookSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.webhooks.DeleteSubscription(r.Context(), id); err != nil {
		if err == bss.ErrSubscriptionNotFound {
			writeError(w, http.StatusNotFound, "ErrNotFound", "webhook subscription not found")
			return
		}
		h.logger.Error("failed to delete webhook subscription", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
