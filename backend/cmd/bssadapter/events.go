// Event replay and webhook delivery operations (I2,
// docs/integration/09-events.md): an integration recovers after downtime by
// replaying its customer's events from a cursor, sees which deliveries
// failed, sends one again, and rotates its signing secret with an overlap.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"acs/internal/bss"
)

type eventPageResponse struct {
	Events         []bss.EventEnvelope `json:"events"`
	NextAfter      int64               `json:"next_after"`
	LatestSequence int64               `json:"latest_sequence"`
	PurgedThrough  int64               `json:"purged_through"`
	// Gap: events after the cursor were removed by retention before they
	// were read; reconcile from current state instead of trusting replay.
	Gap bool `json:"gap"`
}

// listEvents implements GET /bss/v1/events?after=<sequence>&limit=. A
// customer-bound client replays its own customer's stream; a global
// client names one (?customer_id=; empty is the stream of events that
// belong to no customer). Account-listed clients use the TMF688 lists.
func (h *handler) listEvents(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.tmfPrincipal(w, r, bss.ScopeTMFRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	customer := strings.TrimSpace(q.Get("customer_id"))
	switch {
	case claims.CustomerID != "":
		if customer != "" && customer != claims.CustomerID {
			writeError(w, http.StatusNotFound, "ErrNotFound", "resource not found")
			return
		}
		customer = claims.CustomerID
	case !claims.GlobalAccess:
		writeError(w, http.StatusForbidden, "ErrReplayNeedsCustomer", "event replay needs a customer-bound or global client")
		return
	}
	after, err := strconv.ParseInt(firstNonEmpty(q.Get("after"), "0"), 10, 64)
	if err != nil || after < 0 {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "after must be a non-negative sequence number")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	page, err := h.mappings.EventsAfter(r.Context(), customer, after, limit, claims.AccountIDs)
	if err != nil {
		h.logger.Error("failed to replay events", "err", err, "customer_id", customer)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	next := after
	if n := len(page.Events); n > 0 {
		next = page.Events[n-1].Sequence
	}
	writeJSON(w, http.StatusOK, eventPageResponse{Events: page.Events, NextAfter: next, LatestSequence: page.LatestSequence,
		PurgedThrough: page.PurgedThrough, Gap: after < page.PurgedThrough})
}

// subscriptionFor resolves a webhook subscription and authorizes the caller
// for it: an account subscription by its account, a fleet-wide one only for
// a fleet-wide client.
func (h *handler) subscriptionFor(w http.ResponseWriter, r *http.Request, scope string) (*bss.WebhookSubscription, bool) {
	sub, err := h.webhooks.SubscriptionByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, bss.ErrSubscriptionNotFound) {
		writeError(w, http.StatusNotFound, "ErrNotFound", "webhook subscription not found")
		return nil, false
	}
	if err != nil {
		h.logger.Error("failed to resolve webhook subscription", "err", err)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return nil, false
	}
	if sub.AccountID == nil {
		if !h.authorizeBSSFleet(w, r) {
			return nil, false
		}
	} else if !h.authorizeBSSAccount(w, r, scope, *sub.AccountID) {
		return nil, false
	}
	return sub, true
}

// listWebhookDeliveries implements GET /bss/v1/webhooks/{id}/deliveries?status=.
func (h *handler) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	sub, ok := h.subscriptionFor(w, r, bss.ScopeTMFRead)
	if !ok {
		return
	}
	status := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && status != "PENDING" && status != "DELIVERED" && status != "FAILED" {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "status must be PENDING, DELIVERED or FAILED")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.webhooks.ListDeliveries(r.Context(), sub.ID, status, limit)
	if err != nil {
		h.logger.Error("failed to list webhook deliveries", "err", err, "subscription_id", sub.ID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// redeliverWebhook implements POST /bss/v1/webhooks/{id}/deliveries/{delivery_id}/redeliver.
func (h *handler) redeliverWebhook(w http.ResponseWriter, r *http.Request) {
	sub, ok := h.subscriptionFor(w, r, bss.ScopeTMFWrite)
	if !ok {
		return
	}
	deliveryID := r.PathValue("delivery_id")
	if err := h.webhooks.Redeliver(r.Context(), sub.ID, deliveryID); errors.Is(err, bss.ErrDeliveryNotFound) {
		writeError(w, http.StatusNotFound, "ErrNotFound", "webhook delivery not found")
		return
	} else if err != nil {
		h.logger.Error("failed to redeliver webhook", "err", err, "delivery_id", deliveryID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	if err := h.auditor.Record(r.Context(), "bss:webhook", "", "BSSWebhookRedelivered", map[string]any{
		"subscription_id": sub.ID, "delivery_id": deliveryID,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": deliveryID, "status": "PENDING"})
}

const (
	defaultSecretOverlap = 24 * time.Hour
	maxSecretOverlap     = 7 * 24 * time.Hour
)

// rotateWebhookSecret implements POST /bss/v1/webhooks/{id}/rotate-secret.
// The new secret is returned once; deliveries are signed with both secrets
// until the overlap ends (default 24 hours, at most 7 days).
func (h *handler) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	sub, ok := h.subscriptionFor(w, r, bss.ScopeTMFWrite)
	if !ok {
		return
	}
	var req struct {
		OverlapSeconds int `json:"overlap_seconds"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "invalid JSON body")
			return
		}
	}
	overlap := defaultSecretOverlap
	if req.OverlapSeconds != 0 {
		overlap = time.Duration(req.OverlapSeconds) * time.Second
	}
	if overlap < 0 || overlap > maxSecretOverlap {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "overlap_seconds must be between 0 and 604800")
		return
	}
	secret, err := generateWebhookSecret()
	if err != nil {
		h.logger.Error("failed to generate webhook secret", "err", err)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	until, err := h.webhooks.RotateSecret(r.Context(), sub.ID, secret, overlap)
	if err != nil {
		h.logger.Error("failed to rotate webhook secret", "err", err, "subscription_id", sub.ID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	if err := h.auditor.Record(r.Context(), "bss:webhook", "", "BSSWebhookSecretRotated", map[string]any{
		"subscription_id": sub.ID, "previous_valid_until": until,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"id": sub.ID, "secret": secret, "previous_secret_valid_until": until})
}
