// Multi-tenancy REST surface (admin-platform backlog). Structural CRUD
// (regions/customers/projects themselves, operator scope assignment) is
// superadmin-only — this is the org chart, not a day-to-day device action.
// Assigning a device's customer/projects is gated by the curated
// tenancy.manage permission instead, so a manager can be granted it.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"acs/internal/tenancy"
)

type regionResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

func (h *handler) createRegion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	reg, err := h.tenancy.CreateRegion(r.Context(), req.Name)
	if err != nil {
		h.logger.Error("failed to create region", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, regionResponse{ID: reg.ID, Name: reg.Name, CreatedAt: reg.CreatedAt})
}

func (h *handler) listRegions(w http.ResponseWriter, r *http.Request) {
	regs, err := h.tenancy.ListRegions(r.Context())
	if err != nil {
		h.logger.Error("failed to list regions", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]regionResponse, len(regs))
	for i, reg := range regs {
		out[i] = regionResponse{ID: reg.ID, Name: reg.Name, CreatedAt: reg.CreatedAt}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *handler) deleteRegion(w http.ResponseWriter, r *http.Request) {
	if err := h.tenancy.DeleteRegion(r.Context(), r.PathValue("id")); err != nil {
		h.logger.Error("failed to delete region", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type customerResponse struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	RegionID *string `json:"region_id,omitempty"`
}

func (h *handler) createCustomer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string  `json:"name"`
		RegionID *string `json:"region_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	c, err := h.tenancy.CreateCustomer(r.Context(), req.Name, req.RegionID)
	if err != nil {
		h.logger.Error("failed to create customer", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, customerResponse{ID: c.ID, Name: c.Name, RegionID: c.RegionID})
}

func (h *handler) listCustomers(w http.ResponseWriter, r *http.Request) {
	customers, err := h.tenancy.ListCustomers(r.Context())
	if err != nil {
		h.logger.Error("failed to list customers", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]customerResponse, len(customers))
	for i, c := range customers {
		out[i] = customerResponse{ID: c.ID, Name: c.Name, RegionID: c.RegionID}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *handler) deleteCustomer(w http.ResponseWriter, r *http.Request) {
	if err := h.tenancy.DeleteCustomer(r.Context(), r.PathValue("id")); err != nil {
		h.logger.Error("failed to delete customer", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type projectResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (h *handler) createProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	p, err := h.tenancy.CreateProject(r.Context(), req.Name, req.Description)
	if err != nil {
		h.logger.Error("failed to create project", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, projectResponse{ID: p.ID, Name: p.Name, Description: p.Description})
}

func (h *handler) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.tenancy.ListProjects(r.Context())
	if err != nil {
		h.logger.Error("failed to list projects", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]projectResponse, len(projects))
	for i, p := range projects {
		out[i] = projectResponse{ID: p.ID, Name: p.Name, Description: p.Description}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *handler) deleteProject(w http.ResponseWriter, r *http.Request) {
	if err := h.tenancy.DeleteProject(r.Context(), r.PathValue("id")); err != nil {
		h.logger.Error("failed to delete project", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// assignDeviceCustomer sets (or clears with a null/empty customer_id) a
// device's owning customer — the day-to-day tenancy action, gated by the
// curated tenancy.manage permission rather than superadmin-only.
func (h *handler) assignDeviceCustomer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	var req struct {
		CustomerID *string `json:"customer_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := h.tenancy.AssignDeviceCustomer(r.Context(), id, req.CustomerID); err != nil {
		h.logger.Error("failed to assign device customer", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, id, "DeviceCustomerAssigned", map[string]any{"customer_id": req.CustomerID}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) setDeviceProjects(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	var req struct {
		ProjectIDs []string `json:"project_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := h.tenancy.SetDeviceProjects(r.Context(), id, req.ProjectIDs); err != nil {
		h.logger.Error("failed to set device projects", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) getDeviceProjects(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	projects, err := h.tenancy.DeviceProjects(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to get device projects", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]projectResponse, len(projects))
	for i, p := range projects {
		out[i] = projectResponse{ID: p.ID, Name: p.Name, Description: p.Description}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// --- operator scope assignment (superadmin-only) ---

type scopeDTO struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (h *handler) getOperatorScopes(w http.ResponseWriter, r *http.Request) {
	scopes, err := h.tenancy.OperatorScopes(r.Context(), r.PathValue("id"))
	if err != nil {
		h.logger.Error("failed to get operator scopes", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]scopeDTO, len(scopes))
	for i, s := range scopes {
		out[i] = scopeDTO{Type: s.Type, ID: s.ID}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *handler) setOperatorScopes(w http.ResponseWriter, r *http.Request) {
	operatorID := r.PathValue("id")
	var req struct {
		Scopes []scopeDTO `json:"scopes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	scopes := make([]tenancy.Scope, len(req.Scopes))
	for i, s := range req.Scopes {
		if s.Type != tenancy.ScopeRegion && s.Type != tenancy.ScopeCustomer {
			http.Error(w, `scope type must be "region" or "customer"`, http.StatusBadRequest)
			return
		}
		scopes[i] = tenancy.Scope{Type: s.Type, ID: s.ID}
	}
	if err := h.tenancy.SetOperatorScopes(r.Context(), operatorID, scopes); err != nil {
		h.logger.Error("failed to set operator scopes", "err", err, "operator_id", operatorID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "OperatorScopesChanged", map[string]any{"operator_id": operatorID, "scopes": req.Scopes}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// setOperatorGlobalAccess grants or revokes the explicit OPERATOR_GLOBAL
// entitlement (audit P0.1) — the only way a non-superadmin operator gets
// unrestricted fleet access. Superadmin-only, and always audited: this is
// the highest-leverage authorization grant in the system short of the
// superadmin role itself.
func (h *handler) setOperatorGlobalAccess(w http.ResponseWriter, r *http.Request) {
	operatorID := r.PathValue("id")
	var req struct {
		GlobalAccess bool `json:"global_access"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := h.operators.SetGlobalAccess(r.Context(), operatorID, req.GlobalAccess); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to set operator global access", "err", err, "operator_id", operatorID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "OperatorGlobalAccessChanged", map[string]any{"operator_id": operatorID, "global_access": req.GlobalAccess}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
