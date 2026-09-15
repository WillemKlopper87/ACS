package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"acs/internal/bss"
)

const tmf638ServiceBasePath = "/tmf-api/serviceInventoryManagement/v4/service/"

// getTMF638Service exposes the current account-device assignment as a
// read-only TMF638 Service resource. The mapping repository remains the
// source of truth; this endpoint does not create a second inventory store.
func (h *handler) getTMF638Service(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	mapping, err := h.mappings.GetMappingByID(r.Context(), id)
	if errors.Is(err, bss.ErrMappingNotFound) {
		writeError(w, http.StatusNotFound, "ErrNotFound", "no such service")
		return
	}
	if err != nil {
		h.logger.Error("failed to resolve TMF638 service", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	service, err := h.serviceFromMapping(r, mapping)
	if err != nil {
		h.logger.Error("failed to build TMF638 service", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	service.Href = tmf638ServiceBasePath + service.ID
	writeJSON(w, http.StatusOK, tmf638SelectFields(service, r.URL.Query().Get("fields")))
}

// listTMF638Services lists the currently assigned services for one account.
// accountId is required because this adapter deliberately never exposes an
// unscoped cross-tenant inventory query.
func (h *handler) listTMF638Services(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID == "" {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "accountId query parameter is required")
		return
	}
	mappings, err := h.mappings.ListByAccount(r.Context(), accountID)
	if err != nil {
		h.logger.Error("failed to list TMF638 services", "err", err, "account_id", accountID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	requestedRole := strings.TrimSpace(r.URL.Query().Get("serviceCharacteristic.role"))
	requestedState := strings.TrimSpace(r.URL.Query().Get("state"))
	services := make([]tmfService, 0, len(mappings))
	for i := range mappings {
		if requestedRole != "" && mappings[i].Role != requestedRole {
			continue
		}
		if requestedState != "" && tmf638State(mappings[i].Status) != requestedState {
			continue
		}
		service, err := h.serviceFromMapping(r, &mappings[i])
		if err != nil {
			h.logger.Error("failed to build TMF638 service", "err", err, "id", mappings[i].ID)
			writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
			return
		}
		service.Href = tmf638ServiceBasePath + service.ID
		services = append(services, service)
	}
	total := len(services)
	offset, limit := 0, 50
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if n, e := strconv.Atoi(raw); e == nil && n >= 0 {
			offset = n
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, e := strconv.Atoi(raw); e == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		limit = 500
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := services[offset:end]
	selected := make([]any, 0, len(page))
	for _, service := range page {
		selected = append(selected, tmf638SelectFields(service, r.URL.Query().Get("fields")))
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	w.Header().Set("X-Result-Count", strconv.Itoa(len(page)))
	writeJSON(w, http.StatusOK, selected)
}

func tmf638SelectFields(service tmfService, raw string) any {
	if strings.TrimSpace(raw) == "" {
		return service
	}
	allowed := map[string]bool{"id": true, "href": true, "category": true, "state": true, "serviceCharacteristic": true, "relatedParty": true}
	data, _ := json.Marshal(service)
	var all map[string]any
	_ = json.Unmarshal(data, &all)
	selected := map[string]any{}
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if allowed[field] {
			selected[field] = all[field]
		}
	}
	return selected
}

func tmf638State(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PENDING_ACTIVE":
		return "reserved"
	case "ACTIVE":
		return "active"
	case "SUSPENDED":
		return "inactive"
	case "TERMINATED":
		return "terminated"
	default:
		return ""
	}
}
