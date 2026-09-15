package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"acs/internal/alerting"
)

func (h *handler) listAlertPolicies(w http.ResponseWriter, r *http.Request) {
	items, err := h.alertPolicies.List(r.Context())
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	if items == nil {
		items = []alerting.Policy{}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (h *handler) createAlertPolicy(w http.ResponseWriter, r *http.Request) {
	var p alerting.Policy
	if json.NewDecoder(r.Body).Decode(&p) != nil || p.Name == "" || p.Scope == "" {
		http.Error(w, "name and scope are required", 400)
		return
	}
	if p.OfflineAfter < 0 {
		http.Error(w, "offline_after cannot be negative", 400)
		return
	}
	p.Enabled = true
	created, err := h.alertPolicies.Create(r.Context(), p)
	if err != nil {
		h.logger.Error("failed to create alert policy", "err", err)
		http.Error(w, "internal error", 500)
		return
	}
	writeJSON(w, 201, created)
}

func (h *handler) deleteAlertPolicy(w http.ResponseWriter, r *http.Request) {
	if err := h.alertPolicies.Delete(r.Context(), r.PathValue("id")); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", 404)
	} else if err != nil {
		http.Error(w, "internal error", 500)
	} else {
		w.WriteHeader(204)
	}
}
