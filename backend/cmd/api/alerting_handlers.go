package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
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
	if err := validateAlertPolicy(p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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

func validateAlertPolicy(p alerting.Policy) error {
	switch p.Scope {
	case alerting.ScopeFleet:
		if p.TenantID != "" || p.GroupID != "" || p.DeviceID != "" {
			return fmt.Errorf("fleet policy cannot have a target")
		}
	case alerting.ScopeTenant:
		if p.TenantID == "" || p.GroupID != "" || p.DeviceID != "" {
			return fmt.Errorf("tenant policy requires only tenant_id")
		}
	case alerting.ScopeGroup:
		if p.GroupID == "" || p.TenantID == "" || p.DeviceID != "" {
			return fmt.Errorf("group policy requires tenant_id and group_id")
		}
		if _, err := uuid.Parse(p.GroupID); err != nil {
			return fmt.Errorf("group_id must be a UUID")
		}
	case alerting.ScopeDevice:
		if p.DeviceID == "" || p.TenantID == "" || p.GroupID != "" {
			return fmt.Errorf("device policy requires tenant_id and device_id")
		}
		if _, err := uuid.Parse(p.DeviceID); err != nil {
			return fmt.Errorf("device_id must be a UUID")
		}
	default:
		return fmt.Errorf("invalid alert policy scope")
	}
	if p.OfflineAfter < 0 {
		return fmt.Errorf("offline_after cannot be negative")
	}
	for code, priority := range p.FaultPriorities {
		if priority < alerting.P1 || priority > alerting.P4 {
			return fmt.Errorf("invalid priority for %s", code)
		}
	}
	for _, step := range p.Steps {
		if step.After < 0 || step.Destination == "" || step.Recipient == "" {
			return fmt.Errorf("each escalation step requires non-negative after, destination, and recipient")
		}
	}
	return nil
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
