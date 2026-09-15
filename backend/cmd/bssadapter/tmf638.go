package main

import (
	"errors"
	"net/http"
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
	writeJSON(w, http.StatusOK, service)
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
	services := make([]tmfService, 0, len(mappings))
	for i := range mappings {
		service, err := h.serviceFromMapping(r, &mappings[i])
		if err != nil {
			h.logger.Error("failed to build TMF638 service", "err", err, "id", mappings[i].ID)
			writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
			return
		}
		service.Href = tmf638ServiceBasePath + service.ID
		services = append(services, service)
	}
	writeJSON(w, http.StatusOK, services)
}
