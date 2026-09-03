// Config templates — nice-to-have feature backlog, built directly from a
// concrete ask: a single named, reusable, multi-parameter template (e.g.
// a WiFi profile — SSID + passphrase + channel together, not one
// parameter at a time) that can be bulk-applied to an arbitrary device
// selection or a device_groups group, reusing Fleet Control's existing
// selection UI rather than inventing a second one.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"acs/internal/jobs"
	"acs/internal/templates"
)

type templateResponse struct {
	ID          string                     `json:"id"`
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Parameters  []templates.ParameterWrite `json:"parameters"`
	ModelFilter *string                    `json:"model_filter,omitempty"`
	AutoApply   bool                       `json:"auto_apply"`
	CustomerID  *string                    `json:"customer_id,omitempty"`
	CreatedAt   string                     `json:"created_at"`
}

func toTemplateResponse(t *templates.Template) templateResponse {
	return templateResponse{
		ID: t.ID, Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		ModelFilter: t.ModelFilter, AutoApply: t.AutoApply, CustomerID: t.CustomerID, CreatedAt: t.CreatedAt.Format(time.RFC3339),
	}
}

// scopedTemplate loads a template and enforces the caller's tenancy scope
// (audit P0.4/H-3) — 404 both when it doesn't exist and when it's outside
// scope, same reasoning as scopedGroup/getScopedDevice.
func (h *handler) scopedTemplate(w http.ResponseWriter, r *http.Request, id string) (*templates.Template, bool) {
	t, err := h.templates.ByID(r.Context(), id)
	if errors.Is(err, templates.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		h.logger.Error("failed to get config template", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if scoped && !deviceInScope(t.CustomerID, customerIDs) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	return t, true
}

type createTemplateRequest struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Parameters  []templates.ParameterWrite `json:"parameters"`
	ModelFilter *string                    `json:"model_filter,omitempty"`
	AutoApply   bool                       `json:"auto_apply,omitempty"`
	CustomerID  *string                    `json:"customer_id,omitempty"`
}

func (h *handler) createTemplate(w http.ResponseWriter, r *http.Request) {
	var req createTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || len(req.Parameters) == 0 {
		http.Error(w, "name and at least one parameter are required", http.StatusBadRequest)
		return
	}
	if req.AutoApply && (req.ModelFilter == nil || *req.ModelFilter == "") {
		http.Error(w, "auto_apply requires a non-empty model_filter — applying to every device on first contact is not supported", http.StatusBadRequest)
		return
	}

	// audit P0.4: same rule as device groups — a scoped operator must
	// name a customer_id within their own scope, never fall back to a
	// platform-global template by omission.
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if scoped {
		if req.CustomerID == nil || !deviceInScope(req.CustomerID, customerIDs) {
			http.Error(w, "customer_id is required and must be within your assigned scope", http.StatusBadRequest)
			return
		}
	}

	t, err := h.templates.Create(r.Context(), req.Name, req.Description, req.Parameters, req.ModelFilter, req.AutoApply, req.CustomerID, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to create config template", "err", err, "name", req.Name)
		http.Error(w, "internal error (name must be unique)", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "ConfigTemplateCreated", map[string]any{
		"template_id": t.ID, "name": t.Name, "parameter_count": len(t.Parameters), "auto_apply": t.AutoApply, "customer_id": t.CustomerID,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusCreated, toTemplateResponse(t))
}

func (h *handler) listTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := h.templates.List(r.Context())
	if err != nil {
		h.logger.Error("failed to list config templates", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]templateResponse, 0, len(list))
	for _, t := range list {
		if scoped && !deviceInScope(t.CustomerID, customerIDs) {
			continue
		}
		items = append(items, toTemplateResponse(&t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.scopedTemplate(w, r, id); !ok {
		return
	}
	if err := h.templates.Delete(r.Context(), id); err != nil {
		if errors.Is(err, templates.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to delete config template", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "ConfigTemplateDeleted", map[string]any{"template_id": id}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

type applyTemplateRequest struct {
	DeviceIDs []string `json:"device_ids,omitempty"`
	GroupID   string   `json:"group_id,omitempty"`
}

// applyTemplate fans a template's full parameter set out to a device
// selection or group — the same per-device independent-job,
// per-device-outcome shape bulkAction already established for Fleet
// Control, applied here to a named multi-parameter template instead of
// one ad hoc parameter.
func (h *handler) applyTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := h.scopedTemplate(w, r, id)
	if !ok {
		return
	}

	var req applyTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.DeviceIDs) == 0 && req.GroupID != "" {
		memberIDs, err := h.groups.MemberDeviceIDs(r.Context(), req.GroupID)
		if err != nil {
			h.logger.Error("failed to resolve device group", "err", err, "group_id", req.GroupID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		req.DeviceIDs = memberIDs
	}
	if len(req.DeviceIDs) == 0 {
		http.Error(w, "device_ids (or a non-empty group_id) must be provided", http.StatusBadRequest)
		return
	}
	if len(req.DeviceIDs) > maxBulkDevices {
		http.Error(w, fmt.Sprintf("device_ids exceeds the %d-device limit per request", maxBulkDevices), http.StatusBadRequest)
		return
	}

	params := make([]jobs.ParameterWrite, len(t.Parameters))
	for i, p := range t.Parameters {
		params[i] = jobs.ParameterWrite{Name: p.Name, Value: p.Value, Type: p.Type}
	}
	payload := jobs.SetParameterPayload{Parameters: params}

	// audit H-3: applyTemplate used to queue SET_PARAMETER (including
	// WiFi SSID/passphrase writes) against any device_ids/group members
	// with zero scope check — a direct cross-tenant config-write. Every
	// target must both be in the caller's own scope and share the
	// template's customer, the same pattern group membership enforces.
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	operator := operatorFromRequest(r)
	results := make([]bulkActionResult, 0, len(req.DeviceIDs))
	succeeded := 0
	for _, deviceID := range req.DeviceIDs {
		result := bulkActionResult{DeviceID: deviceID}
		d, err := h.devices.Get(r.Context(), deviceID)
		if err != nil || (scoped && !deviceInScope(d.CustomerID, customerIDs)) || !sameCustomer(d.CustomerID, t.CustomerID) {
			result.Error = "not found"
			results = append(results, result)
			continue
		}
		job, err := h.jobs.Create(r.Context(), deviceID, jobs.TypeSetParameter, payload, "template:"+t.Name+" ("+operator+")")
		if err != nil {
			result.Error = err.Error()
		} else {
			result.CommandKey = job.CommandKey
			succeeded++
		}
		results = append(results, result)
	}

	if err := h.auditor.Record(r.Context(), operator, "", "ConfigTemplateApplied", map[string]any{
		"template_id": t.ID, "name": t.Name, "requested": len(req.DeviceIDs), "succeeded": succeeded,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("config template applied", "template_id", t.ID, "name", t.Name, "requested", len(req.DeviceIDs), "succeeded", succeeded)

	writeJSON(w, http.StatusOK, map[string]any{
		"template_id": t.ID, "requested": len(req.DeviceIDs), "succeeded": succeeded, "results": results,
	})
}
