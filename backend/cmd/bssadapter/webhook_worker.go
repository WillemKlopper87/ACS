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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"acs/internal/bss"
)

const (
	webhookNotifyInterval  = 10 * time.Second
	webhookDeliverInterval = 10 * time.Second
	webhookBatchSize       = 50
	webhookHTTPTimeout     = 10 * time.Second
)

func (h *handler) runTMFEventDispatchLoop(ctx context.Context) {
	// A bounded lookback covers events written while the adapter was stopped;
	// tmf_webhook_dispatches makes replay of the lookback harmless.
	ticker := time.NewTicker(webhookNotifyInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := h.webhooks.DispatchTMFEvents(ctx, time.Now().UTC().Add(-24*time.Hour), webhookBatchSize)
			if err != nil {
				h.logger.Error("failed to dispatch TMF events to webhook hubs", "err", err)
			}
			if n > 0 {
				h.logger.Info("TMF events queued for webhook delivery", "count", n)
			}
		}
	}
}

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
	// target_url is BSS-operator-controlled (audit H-7): DialControl
	// re-enforces netPolicy at connect time (rebinding-proof) behind the
	// up-front CheckHost in sendWebhookDelivery. Redirects are never
	// followed — a webhook receiver has no legitimate reason to 3xx a
	// signed delivery, and following one would retarget it (with the
	// HMAC signature already computed for the original body) wherever
	// the up-front check never saw.
	client := &http.Client{
		Timeout: webhookHTTPTimeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: webhookHTTPTimeout,
				Control: h.netPolicy.DialControl,
			}).DialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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

// sendWebhookDelivery POSTs one delivery, signed with a scheme modelled on
// Standard Webhooks: Webhook-Signature is "v1," + hex(HMAC-SHA256(secret,
// "<id>.<timestamp>.<body>")). Binding the delivery id and the send-time
// timestamp into the signed string, not just the body, is what lets a
// receiver reject a captured-and-replayed delivery and dedupe legitimate
// retries — a body-only HMAC (the previous scheme) gives it neither.
//
// This is not wire-compatible with stock Standard Webhooks verifiers: we
// hex-encode the MAC and use a plain shared secret, where Standard
// Webhooks base64-encodes it and expects a whsec_-prefixed, base64-decoded
// secret. Only the signed-string construction and header names match.
func (h *handler) sendWebhookDelivery(ctx context.Context, client *http.Client, d bss.WebhookDelivery) bool {
	// audit H-7: re-checked at send time, not just at subscription
	// creation — a target allowed when the subscription was created
	// shouldn't be trusted forever if the policy tightens later.
	if u, err := url.Parse(d.TargetURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		h.logger.Warn("webhook delivery target has an invalid scheme, refusing", "delivery_id", d.ID, "target_url", d.TargetURL)
		return false
	} else if err := h.netPolicy.CheckHost(ctx, u.Hostname()); err != nil {
		h.logger.Warn("webhook delivery target rejected by network policy", "err", err, "delivery_id", d.ID, "target_url", d.TargetURL)
		return false
	}

	// Fresh timestamp per attempt, not per delivery: a retry an hour later
	// must still land inside the consumer's freshness window, while any
	// captured copy of an earlier attempt falls outside it.
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signature := webhookSignature(d.Secret, d.ID, timestamp, d.Payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TargetURL, bytes.NewReader(d.Payload))
	if err != nil {
		h.logger.Error("failed to build webhook delivery request", "err", err, "delivery_id", d.ID)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Webhook-Id", d.ID)
	req.Header.Set("Webhook-Timestamp", timestamp)
	req.Header.Set("Webhook-Signature", "v1,"+signature)
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

// webhookSecretKeyMaterial resolves the HMAC key and output encoding for a
// subscription secret. A secret of the form "whsec_<base64>" is the actual
// Standard Webhooks secret convention (the dashboard-generated form every
// svix/standardwebhooks client library expects) -- when present, its
// base64 payload is decoded to raw key bytes and the signature is
// base64-encoded, making delivery fully wire-compatible with an
// off-the-shelf Standard Webhooks verifier, not just its header names and
// signed-string construction.
//
// A secret without that prefix is used exactly as before (raw string
// bytes, hex-encoded output) -- every subscription created before this
// existed keeps verifying the same way it always has; nothing breaks
// retroactively. A malformed whsec_ secret (bad base64) also falls back to
// this path rather than failing delivery outright, since a garbled secret
// will fail verification either way and this at least keeps trying.
func webhookSecretKeyMaterial(secret string) (key []byte, base64Output bool) {
	if trimmed, ok := strings.CutPrefix(secret, "whsec_"); ok {
		if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
			return decoded, true
		}
	}
	return []byte(secret), false
}

// webhookSignature signs a delivery using Standard Webhooks' signed-string
// construction: HMAC-SHA256 over "<msg-id>.<timestamp>.<payload>". Binding
// the id and timestamp into the signed string, not just the body, is what
// makes the signature non-replayable and lets a receiver dedupe retries --
// a body-only HMAC (this worker's original scheme) gives it neither.
//
// The output encoding depends on the secret (see webhookSecretKeyMaterial):
// a whsec_-format secret gets the real Standard Webhooks wire format
// (base64 MAC, base64-decoded key); any other secret keeps the hex
// encoding this worker has always used, for backward compatibility.
func webhookSignature(secret, msgID, timestamp string, payload []byte) string {
	key, base64Output := webhookSecretKeyMaterial(secret)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msgID))
	mac.Write([]byte("."))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	sum := mac.Sum(nil)
	if base64Output {
		return base64.StdEncoding.EncodeToString(sum)
	}
	return hex.EncodeToString(sum)
}

// generateWebhookSecret returns a fresh Standard-Webhooks-format secret
// (24 random bytes, base64-encoded, whsec_-prefixed) -- the same shape
// svix's own dashboard generates. Offered so an integrator creating a
// subscription can opt into full Standard Webhooks wire compatibility
// without hand-rolling the format themselves; a caller is always free to
// supply their own secret (whsec_-prefixed or not) instead.
func generateWebhookSecret() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(raw), nil
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
	if req.AccountID == nil {
		if !h.authorizeBSSFleet(w, r) {
			return
		}
	} else if !h.authorizeBSSAccount(w, r, *req.AccountID) {
		return
	}
	// audit H-7: validate at save time too, not only at delivery.
	if u, err := url.Parse(req.TargetURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "target_url must be a valid http/https URL")
		return
	} else if err := h.netPolicy.CheckHost(r.Context(), u.Hostname()); err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "target_url host is not allowed: "+err.Error())
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
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	var (
		subs []bss.WebhookSubscription
		err  error
	)
	if accountID == "" {
		if !h.authorizeBSSFleet(w, r) {
			return
		}
		subs, err = h.webhooks.ListSubscriptions(r.Context())
	} else {
		if !h.authorizeBSSAccount(w, r, accountID) {
			return
		}
		subs, err = h.webhooks.ListSubscriptionsForAccount(r.Context(), accountID)
	}
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
	sub, err := h.webhooks.SubscriptionByID(r.Context(), id)
	if errors.Is(err, bss.ErrSubscriptionNotFound) {
		writeError(w, http.StatusNotFound, "ErrNotFound", "webhook subscription not found")
		return
	}
	if err != nil {
		h.logger.Error("failed to resolve webhook subscription", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	if sub.AccountID == nil {
		if !h.authorizeBSSFleet(w, r) {
			return
		}
	} else if !h.authorizeBSSAccount(w, r, *sub.AccountID) {
		return
	}
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
