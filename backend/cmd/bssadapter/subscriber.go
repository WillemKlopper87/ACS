package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"acs/internal/bss"
	"acs/internal/subscribers"
)

// The BSS is the source of truth for a subscriber's display name and may
// set the VIP flag (internal/subscribers): on a mapping or sale
// (subscriber_name, vip) or on its own with PUT
// /bss/v1/accounts/{account_id}/subscriber. A name the BSS sends replaces
// one an operator typed. Operators use the console to look subscribers up.

const maxSubscriberName = 120

// applySubscriber writes name/vip for accountID when either is set; it
// answers the request itself and reports false on failure.
func (h *handler) applySubscriber(w http.ResponseWriter, r *http.Request, accountID string, name *string, vip *bool) bool {
	if name == nil && vip == nil {
		return true
	}
	if name != nil && len(strings.TrimSpace(*name)) > maxSubscriberName {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "subscriber_name is at most 120 characters")
		return false
	}
	if _, err := h.subscribers.Set(r.Context(), accountID, subscribers.SourceBSS, "bss:"+accountID, name, vip); err != nil {
		h.logger.Error("failed to set subscriber profile", "err", err, "account_id", accountID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return false
	}
	return true
}

// putAccountSubscriber is PUT /bss/v1/accounts/{account_id}/subscriber
// (tmf:write): {"subscriber_name": "...", "vip": true}. An empty name
// clears it.
func (h *handler) putAccountSubscriber(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("account_id")
	var req struct {
		SubscriberName *string `json:"subscriber_name"`
		VIP            *bool   `json:"vip"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || (req.SubscriberName == nil && req.VIP == nil) {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "send subscriber_name and/or vip")
		return
	}
	if !h.authorizeBSSAccount(w, r, bss.ScopeTMFWrite, accountID) {
		return
	}
	if !h.applySubscriber(w, r, accountID, req.SubscriberName, req.VIP) {
		return
	}
	p, err := h.subscribers.Get(r.Context(), accountID)
	if err != nil || p == nil {
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account_id": accountID, "subscriber_name": p.DisplayName, "vip": p.VIP})
}
