package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"acs/internal/subscribers"
)

// Subscriber lookup and account profiles (internal/subscribers): find the
// CPE of a customer who calls in by name, address, account number, the
// last digits of a serial, a device label, or the VIP list.

// lookupSubscribers is GET /api/v1/lookup?q=&vip=1&limit=. Results are
// limited to the caller's customers. A lookup that matched on a name or
// an address is personal information and is audited (who searched for
// what, and how many results), never the results themselves.
func (h *handler) lookupSubscribers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	vipOnly := r.URL.Query().Get("vip") == "1" || r.URL.Query().Get("vip") == "true"
	if len(q) > 100 {
		http.Error(w, "q is at most 100 characters", http.StatusBadRequest)
		return
	}
	if q == "" && !vipOnly {
		http.Error(w, "send q (name, address, account, serial digits or label) or vip=1", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	hits, err := h.subscribers.Lookup(r.Context(), subscribers.LookupParams{Query: q, VIPOnly: vipOnly, CustomerIDs: customerIDs, Scoped: scoped, Limit: limit})
	if err != nil {
		h.logger.Error("subscriber lookup failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if hits == nil {
		hits = []subscribers.Hit{}
	}
	if subscribers.PersonalMatch(hits) || (len(hits) == 0 && strings.IndexFunc(q, func(c rune) bool { return c >= 'A' && c <= 'z' }) >= 0) || vipOnly {
		if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "SubscriberLookup", map[string]any{
			"query": q, "vip_only": vipOnly, "results": len(hits),
		}); err != nil {
			h.logger.Error("failed to write audit record", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": hits, "count": len(hits)})
}

// accountInScope reports whether the caller may see an account: one of
// its customers is in the caller's scope (unscoped callers see all).
func (h *handler) accountInScope(w http.ResponseWriter, r *http.Request, accountID string) bool {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if !scoped {
		return true
	}
	owners, err := h.subscribers.AccountCustomers(r.Context(), accountID)
	if err != nil {
		h.logger.Error("failed to resolve account customers", "err", err, "account_id", accountID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	for _, o := range owners {
		for _, c := range customerIDs {
			if o == c {
				return true
			}
		}
	}
	http.Error(w, "not found", http.StatusNotFound)
	return false
}

// getAccountProfile is GET /api/v1/accounts/{account_id}/profile.
func (h *handler) getAccountProfile(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("account_id")
	if !h.accountInScope(w, r, accountID) {
		return
	}
	p, err := h.subscribers.Get(r.Context(), accountID)
	if err != nil {
		h.logger.Error("failed to read account profile", "err", err, "account_id", accountID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if p == nil {
		p = &subscribers.Profile{AccountID: accountID}
	}
	writeJSON(w, http.StatusOK, p)
}

// putAccountProfile is PUT /api/v1/accounts/{account_id}/profile
// (subscriber.manage): an operator fills in a subscriber name the BSS has
// not sent (409 when it has), or sets or clears VIP.
func (h *handler) putAccountProfile(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("account_id")
	if !h.accountInScope(w, r, accountID) {
		return
	}
	var req struct {
		SubscriberName *string `json:"subscriber_name"`
		VIP            *bool   `json:"vip"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || (req.SubscriberName == nil && req.VIP == nil) {
		http.Error(w, "send subscriber_name and/or vip", http.StatusBadRequest)
		return
	}
	if req.SubscriberName != nil && len(strings.TrimSpace(*req.SubscriberName)) > 120 {
		http.Error(w, "subscriber_name is at most 120 characters", http.StatusBadRequest)
		return
	}
	actor := operatorFromRequest(r)
	p, err := h.subscribers.Set(r.Context(), accountID, subscribers.SourceOperator, actor, req.SubscriberName, req.VIP)
	if errors.Is(err, subscribers.ErrNameFromBSS) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		h.logger.Error("failed to update account profile", "err", err, "account_id", accountID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	details := map[string]any{"account_id": accountID, "name_changed": req.SubscriberName != nil}
	if req.VIP != nil {
		details["vip"] = *req.VIP
	}
	if err := h.auditor.Record(r.Context(), actor, "", "AccountProfileChanged", details); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusOK, p)
}

// attachSubscribers adds each device's subscriber summary to a response.
func (h *handler) attachSubscribers(r *http.Request, items []deviceResponse) {
	if h.subscribers == nil || len(items) == 0 {
		return
	}
	ids := make([]string, len(items))
	for i, d := range items {
		ids[i] = d.ID
	}
	sums, err := h.subscribers.Summaries(r.Context(), ids)
	if err != nil {
		h.logger.Warn("failed to read subscriber summaries", "err", err)
		return
	}
	for i := range items {
		if s, ok := sums[items[i].ID]; ok {
			s := s
			items[i].Subscriber = &s
		}
	}
}
