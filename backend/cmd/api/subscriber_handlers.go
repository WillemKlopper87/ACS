package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"acs/internal/subscribers"
)

// Subscriber lookup and account profiles (internal/subscribers): find the
// CPE of a customer who calls in by name, address, account number, the
// last digits of a serial, a device label, or the VIP list.

// lookupSubscribers is GET /api/v1/lookup?q=&vip=1&limit=. Results are
// limited to the caller's customers. Every lookup is audited (who
// searched for what, what it matched on, how many results), never the
// results themselves: even a serial search returns names and addresses.
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
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "SubscriberLookup", map[string]any{
		"query": q, "vip_only": vipOnly, "results": len(hits), "personal_match": subscribers.PersonalMatch(hits),
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
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
	// Every customer that decides access must be in scope; an account
	// with none (no owner, no mapped device) is visible only unscoped.
	inScope := len(owners) > 0
	for _, o := range owners {
		if !slices.Contains(customerIDs, o) {
			inScope = false
		}
	}
	if !inScope {
		http.Error(w, "not found", http.StatusNotFound)
	}
	return inScope
}

// getAccountProfile is GET /api/v1/accounts/{account_id}/profile.
func (h *handler) getAccountProfile(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("account_id")
	r, ok := h.namedAccount(w, r, accountID)
	if !ok || !h.accountInScope(w, r, accountID) {
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
	r, ok := h.namedAccount(w, r, accountID)
	if !ok || !h.accountInScope(w, r, accountID) {
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

// revealDeviceSubscriber is POST /api/v1/devices/{id}/subscriber/reveal:
// the device's subscriber with the full site address, which device reads
// leave out. Every reveal is audited (who, which device and account,
// never the details themselves).
func (h *handler) revealDeviceSubscriber(w http.ResponseWriter, r *http.Request) {
	d, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	sums, err := h.subscribers.Summaries(r.Context(), []string{d.ID})
	if err != nil {
		h.logger.Error("failed to read subscriber summary", "err", err, "device_id", d.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s, ok := sums[d.ID]
	if !ok || s.AccountID == "" {
		http.Error(w, "this device is not linked to an account", http.StatusNotFound)
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), d.ID, "SubscriberDetailsRevealed", map[string]any{
		"account_id": s.AccountID,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// withoutAddress keeps the suburb and city of a site, which identify the
// area, and drops what identifies the home (site name, street, postal
// code); those come only from the audited reveal.
func withoutAddress(s *subscribers.Site) *subscribers.Site {
	if s == nil || (s.Suburb == "" && s.City == "") {
		return nil
	}
	return &subscribers.Site{Suburb: s.Suburb, City: s.City}
}

// attachSubscribers adds each device's subscriber summary to a response,
// without the street address (see withoutAddress).
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
			s.Site = withoutAddress(s.Site)
			items[i].Subscriber = &s
		}
	}
}
