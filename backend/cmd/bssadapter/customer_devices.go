package main

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"acs/internal/bss"
)

type customerDevicesResponse struct {
	Items []bss.CustomerDevice `json:"items"`
	// NextCursor fetches the next page; absent on the last one.
	NextCursor string `json:"next_cursor,omitempty"`
}

// listCustomerDevices is GET /bss/v1/devices?cursor=&limit=: the caller's
// customer's devices with their current account and reachability, in
// pages. The cursor is opaque. A global client names the customer with
// customer_id; an account-listed client is refused (it uses the mappings).
func (h *handler) listCustomerDevices(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusForbidden, "ErrClientNotCustomerBound", "the device list needs a customer-bound or global client")
		return
	case customer == "":
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "customer_id is required for a global client")
		return
	}
	after := ""
	if c := q.Get("cursor"); c != "" {
		raw, err := base64.RawURLEncoding.DecodeString(c)
		if err != nil {
			writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "invalid cursor")
			return
		}
		after = string(raw)
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 500 {
		limit = 100
	}
	// One extra row tells whether there is a next page.
	devices, err := h.mappings.CustomerDevices(r.Context(), customer, after, limit+1, claims.AccountIDs)
	if err != nil {
		h.logger.Error("failed to list customer devices", "err", err)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	resp := customerDevicesResponse{Items: devices}
	if len(devices) > limit {
		resp.Items = devices[:limit]
		resp.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(devices[limit-1].OUISerial))
	}
	writeJSON(w, http.StatusOK, resp)
}
