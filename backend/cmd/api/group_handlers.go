// Device groups and tags (build plan §4 Phase 7 / design doc v3 Phase 7:
// "Device groups, Tags"). Groups are the curated, named target
// bulkAction's group_id resolves against; tags are freeform per-device
// labels, replace-the-whole-set on write.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"acs/internal/devices"
)

// --- tags ----------------------------------------------------------------

type updateTagsRequest struct {
	Tags []string `json:"tags"`
}

func (h *handler) updateDeviceTags(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	var req updateTagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if err := h.devices.UpdateTags(r.Context(), id, req.Tags); err != nil {
		h.logger.Error("failed to update device tags", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"device_id": id, "tags": req.Tags})
}

// --- device groups ---------------------------------------------------------

type deviceGroupResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	CustomerID  *string `json:"customer_id,omitempty"`
	MemberCount int     `json:"member_count"`
	CreatedAt   string  `json:"created_at"`
}

func toGroupResponse(g *devices.DeviceGroup) deviceGroupResponse {
	return deviceGroupResponse{
		ID: g.ID, Name: g.Name, Description: g.Description, CustomerID: g.CustomerID,
		MemberCount: g.MemberCount, CreatedAt: g.CreatedAt.Format(time.RFC3339),
	}
}

// scopedGroup loads a group and enforces the caller's tenancy scope
// (audit P0.2/H-3) — 404 both when the group doesn't exist and when it's
// outside scope, matching getScopedDevice's reasoning: a 403 would
// confirm the group's existence across a tenant boundary.
func (h *handler) scopedGroup(w http.ResponseWriter, r *http.Request, id string) (*devices.DeviceGroup, bool) {
	g, err := h.groups.Get(r.Context(), id)
	if errors.Is(err, devices.ErrGroupNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		h.logger.Error("failed to get device group", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if scoped && !deviceInScope(g.CustomerID, customerIDs) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	return g, true
}

type createDeviceGroupRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	CustomerID  *string `json:"customer_id,omitempty"`
}

func (h *handler) createDeviceGroup(w http.ResponseWriter, r *http.Request) {
	var req createDeviceGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	// audit P0.2: a scoped operator must name a customer_id within their
	// own scope — omitting it must never fall back to a platform-global
	// group, which only an unscoped (superadmin/GlobalAccess) caller may
	// create.
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

	group, err := h.groups.Create(r.Context(), req.Name, req.Description, req.CustomerID)
	if errors.Is(err, devices.ErrGroupNameUsed) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		h.logger.Error("failed to create device group", "err", err, "name", req.Name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "DeviceGroupCreated", map[string]any{
		"group_id": group.ID, "name": group.Name, "customer_id": group.CustomerID,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	writeJSON(w, http.StatusCreated, toGroupResponse(group))
}

func (h *handler) listDeviceGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.groups.List(r.Context())
	if err != nil {
		h.logger.Error("failed to list device groups", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	items := make([]deviceGroupResponse, 0, len(groups))
	for _, g := range groups {
		if scoped && !deviceInScope(g.CustomerID, customerIDs) {
			continue
		}
		items = append(items, toGroupResponse(&g))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) getDeviceGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	group, ok := h.scopedGroup(w, r, id)
	if !ok {
		return
	}

	memberIDs, err := h.groups.MemberDeviceIDs(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to list device group members", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := toGroupResponse(group)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": resp.ID, "name": resp.Name, "description": resp.Description, "customer_id": resp.CustomerID,
		"member_count": resp.MemberCount, "created_at": resp.CreatedAt,
		"device_ids": memberIDs,
	})
}

func (h *handler) deleteDeviceGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.scopedGroup(w, r, id); !ok {
		return
	}
	if err := h.groups.Delete(r.Context(), id); errors.Is(err, devices.ErrGroupNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to delete device group", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "DeviceGroupDeleted", map[string]any{
		"group_id": id,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

type addGroupMembersRequest struct {
	DeviceIDs []string `json:"device_ids"`
}

func (h *handler) addDeviceGroupMembers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	group, ok := h.scopedGroup(w, r, id)
	if !ok {
		return
	}
	var req addGroupMembersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.DeviceIDs) == 0 {
		http.Error(w, "device_ids must not be empty", http.StatusBadRequest)
		return
	}

	// audit H-3: every device added must itself be in the caller's scope
	// AND belong to the group's own customer — otherwise a group becomes
	// a cross-tenant bulk-action bypass (the protocol's own warning),
	// letting a scoped operator smuggle a foreign device into a group
	// whose templates/schedules will later act on it.
	addable := make([]string, 0, len(req.DeviceIDs))
	for _, deviceID := range req.DeviceIDs {
		d, ok := h.getScopedDevice(w, r, deviceID)
		if !ok {
			return
		}
		if !sameCustomer(d.CustomerID, group.CustomerID) {
			http.Error(w, fmt.Sprintf("device %s does not belong to this group's customer", deviceID), http.StatusBadRequest)
			return
		}
		addable = append(addable, deviceID)
	}

	if err := h.groups.AddMembers(r.Context(), id, addable); err != nil {
		h.logger.Error("failed to add device group members", "err", err, "group_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "DeviceGroupMembersAdded", map[string]any{
		"group_id": id, "device_count": len(addable),
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	group, err := h.groups.Get(r.Context(), id)
	if errors.Is(err, devices.ErrGroupNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to reload device group", "err", err, "group_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toGroupResponse(group))
}

// sameCustomer reports whether a device's customer_id matches a control
// object's owning customer_id — both nil (unassigned device, platform-
// global object) counts as a match; exactly one nil never does.
func sameCustomer(deviceCustomerID, objectCustomerID *string) bool {
	if deviceCustomerID == nil || objectCustomerID == nil {
		return deviceCustomerID == objectCustomerID
	}
	return *deviceCustomerID == *objectCustomerID
}

func (h *handler) removeDeviceGroupMember(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	deviceID := r.PathValue("device_id")
	if _, ok := h.scopedGroup(w, r, groupID); !ok {
		return
	}

	if err := h.groups.RemoveMember(r.Context(), groupID, deviceID); err != nil {
		h.logger.Error("failed to remove device group member", "err", err, "group_id", groupID, "device_id", deviceID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), deviceID, "DeviceGroupMemberRemoved", map[string]any{
		"group_id": groupID,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	w.WriteHeader(http.StatusNoContent)
}
