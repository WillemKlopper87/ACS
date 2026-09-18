package main

import (
	"acs/internal/alerting"
	"database/sql"
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
	items, err = h.filterAlertIncidents(r, items)
	if err != nil {
		h.logger.Error("failed to scope alert incidents", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
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
	incident, err := h.alertIncidents.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, alerting.ErrIncidentNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ok, err := h.alertIncidentInScope(r, *incident)
	if err != nil {
		h.logger.Error("failed to scope alert incident", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := h.alertIncidents.SetState(r.Context(), incident.ID, state, operatorFromRequest(r)); err != nil {
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

func (h *handler) filterAlertIncidents(r *http.Request, incidents []alerting.Incident) ([]alerting.Incident, error) {
	filtered := make([]alerting.Incident, 0, len(incidents))
	for _, incident := range incidents {
		ok, err := h.alertIncidentInScope(r, incident)
		if err != nil {
			return nil, err
		}
		if ok {
			filtered = append(filtered, incident)
		}
	}
	return filtered, nil
}

func (h *handler) alertIncidentInScope(r *http.Request, incident alerting.Incident) (bool, error) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil || !scoped {
		return err == nil, err
	}
	d, err := h.devices.Get(r.Context(), incident.DeviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return deviceInScope(d.CustomerID, customerIDs), nil
}
