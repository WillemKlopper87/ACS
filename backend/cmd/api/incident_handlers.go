package main

import (
	"acs/internal/alerting"
	"errors"
	"net/http"
	"strings"
)

func (h *handler) listAlertIncidents(w http.ResponseWriter, r *http.Request) {
	items, err := h.alertIncidents.List(r.Context(), strings.TrimSpace(r.URL.Query().Get("state")))
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	if items == nil {
		items = []alerting.Incident{}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (h *handler) updateAlertIncident(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		http.Error(w, "state is required", 400)
		return
	}
	if err := h.alertIncidents.SetState(r.Context(), r.PathValue("id"), state, operatorFromRequest(r)); err != nil {
		if errors.Is(err, alerting.ErrIncidentNotFound) {
			http.Error(w, "not found", 404)
		} else if errors.Is(err, alerting.ErrInvalidIncidentState) {
			http.Error(w, "invalid state transition", 409)
		} else {
			http.Error(w, "internal error", 500)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
