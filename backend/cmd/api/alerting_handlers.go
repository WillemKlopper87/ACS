package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"net/http"

	"acs/internal/alerting"
	"acs/internal/devices"
)

func (h *handler) listAlertPolicies(w http.ResponseWriter, r *http.Request) {
	items, err := h.alertPolicies.List(r.Context())
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	items, err = h.filterAlertPolicies(r, items)
	if err != nil {
		h.logger.Error("failed to scope alert policies", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
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
	if ok, err := h.alertPolicyInScope(r, p); err != nil {
		h.logger.Error("failed to scope alert policy", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !ok {
		http.Error(w, "not found", http.StatusNotFound)
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
		if p.GroupID == "" || p.TenantID != "" || p.DeviceID != "" {
			return fmt.Errorf("group policy requires only group_id")
		}
		if _, err := uuid.Parse(p.GroupID); err != nil {
			return fmt.Errorf("group_id must be a UUID")
		}
	case alerting.ScopeDevice:
		if p.DeviceID == "" || p.TenantID != "" || p.GroupID != "" {
			return fmt.Errorf("device policy requires only device_id")
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
	p, err := h.alertPolicies.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", 404)
		return
	} else if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	ok, err := h.alertPolicyInScope(r, *p)
	if err != nil {
		h.logger.Error("failed to scope alert policy", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := h.alertPolicies.Delete(r.Context(), p.ID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) filterAlertPolicies(r *http.Request, policies []alerting.Policy) ([]alerting.Policy, error) {
	filtered := make([]alerting.Policy, 0, len(policies))
	for _, p := range policies {
		ok, err := h.alertPolicyInScope(r, p)
		if err != nil {
			return nil, err
		}
		if ok {
			filtered = append(filtered, p)
		}
	}
	return filtered, nil
}

// alertPolicyInScope makes fleet and BSS-account-targeted policies
// platform-global controls. Scoped operators may only manage a group or
// device policy whose owning customer is in their assignment.
func (h *handler) alertPolicyInScope(r *http.Request, p alerting.Policy) (bool, error) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil || !scoped {
		return err == nil, err
	}
	switch p.Scope {
	case alerting.ScopeFleet, alerting.ScopeTenant:
		return false, nil
	case alerting.ScopeGroup:
		g, err := h.groups.Get(r.Context(), p.GroupID)
		if errors.Is(err, devices.ErrGroupNotFound) {
			return false, nil
		}
		return err == nil && deviceInScope(g.CustomerID, customerIDs), err
	case alerting.ScopeDevice:
		d, err := h.devices.Get(r.Context(), p.DeviceID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil && deviceInScope(d.CustomerID, customerIDs), err
	default:
		return false, nil
	}
}
