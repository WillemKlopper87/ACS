// Policy engine REST surface (build plan §4 Phase 7). cmd/acs is what
// actually evaluates and enforces these (cmd/acs/enforce.go, on every
// Inform); this process only manages the definitions.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"acs/internal/policy"
)

type policyResponse struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	ModelFilter   *string `json:"model_filter,omitempty"`
	ParameterName string  `json:"parameter_name"`
	DesiredValue  string  `json:"desired_value"`
	Enabled       bool    `json:"enabled"`
	CreatedAt     string  `json:"created_at"`
}

func toPolicyResponse(p *policy.Policy) policyResponse {
	return policyResponse{
		ID: p.ID, Name: p.Name, ModelFilter: p.ModelFilter,
		ParameterName: p.ParameterName, DesiredValue: p.DesiredValue,
		Enabled: p.Enabled, CreatedAt: p.CreatedAt.Format(time.RFC3339),
	}
}

type createPolicyRequest struct {
	Name          string  `json:"name"`
	ModelFilter   *string `json:"model_filter,omitempty"`
	ParameterName string  `json:"parameter_name"`
	DesiredValue  string  `json:"desired_value"`
}

func (h *handler) createPolicy(w http.ResponseWriter, r *http.Request) {
	var req createPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.ParameterName == "" || req.DesiredValue == "" {
		http.Error(w, "name, parameter_name, and desired_value are required", http.StatusBadRequest)
		return
	}

	p, err := h.policies.Create(r.Context(), req.Name, req.ModelFilter, req.ParameterName, req.DesiredValue, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to create policy", "err", err, "name", req.Name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "PolicyCreated", map[string]any{
		"policy_id": p.ID, "name": p.Name, "parameter_name": p.ParameterName,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	writeJSON(w, http.StatusCreated, toPolicyResponse(p))
}

func (h *handler) listPolicies(w http.ResponseWriter, r *http.Request) {
	list, err := h.policies.List(r.Context())
	if err != nil {
		h.logger.Error("failed to list policies", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]policyResponse, 0, len(list))
	for _, p := range list {
		items = append(items, toPolicyResponse(&p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) deletePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.policies.Delete(r.Context(), id); errors.Is(err, policy.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to delete policy", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "PolicyDeleted", map[string]any{"policy_id": id}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) setPolicyEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p, err := h.policies.SetEnabled(r.Context(), id, enabled)
		if errors.Is(err, policy.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			h.logger.Error("failed to update policy", "err", err, "id", id, "enabled", enabled)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, toPolicyResponse(p))
	}
}
