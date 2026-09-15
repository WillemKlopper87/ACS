// TMF640 (Service Activation and Configuration) shapes, sub-project C-2
// (design docs/superpowers/specs/2026-09-13-bss-tmf640-design.md). A thin
// translation layer over the same account_device_mappings/bss_orders
// data and dispatchOrder sequence /bss/v1/* already uses -- no new
// dispatch logic lives here.
package main

import (
	"errors"
	"net/http"

	"acs/internal/bss"
	"acs/internal/devices/adapters"
)

const (
	tmfServiceBasePath = "/tmf-api/serviceActivationAndConfiguration/v4/service/"
	tmfMonitorBasePath = "/tmf-api/serviceActivationAndConfiguration/v4/monitor/"
)

type tmfServiceCharacteristic struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type tmfRelatedParty struct {
	ID string `json:"id"`
}

type tmfService struct {
	ID                    string                     `json:"id"`
	Href                  string                     `json:"href"`
	Category              string                     `json:"category"`
	State                 string                     `json:"state"`
	ServiceCharacteristic []tmfServiceCharacteristic `json:"serviceCharacteristic"`
	RelatedParty          []tmfRelatedParty          `json:"relatedParty"`
}

// serviceFromMapping builds a Service resource for one mapping, resolving
// SSID's current value via ACSClient.GetParameters. WiFiPassword is
// deliberately never read back: it remains write-only credential material.
func (h *handler) serviceFromMapping(r *http.Request, m *bss.AccountDeviceMapping) (tmfService, error) {
	svc := tmfService{
		ID:           m.ID,
		Href:         tmfServiceBasePath + m.ID,
		Category:     "customer facing service",
		State:        "active",
		RelatedParty: []tmfRelatedParty{{ID: m.AccountID}},
	}

	dev, err := h.acs.GetDevice(r.Context(), m.DeviceID)
	if err != nil {
		return tmfService{}, err
	}
	ssidPath, ssidOK := adapters.ResolvePath(dev.DataModelRoot, adapters.WiFiSSID)
	if !ssidOK {
		return svc, nil
	}

	cached, err := h.acs.GetParameters(r.Context(), m.DeviceID, []string{ssidPath})
	if err != nil {
		return tmfService{}, err
	}
	if v, ok := cached[ssidPath]; ok {
		svc.ServiceCharacteristic = append(svc.ServiceCharacteristic, tmfServiceCharacteristic{Name: "SSID", Value: v.Value})
	}
	return svc, nil
}

// getService implements GET /service/{id}.
func (h *handler) getService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := h.mappings.GetMappingByID(r.Context(), id)
	if errors.Is(err, bss.ErrMappingNotFound) {
		writeError(w, http.StatusNotFound, "ErrNotFound", "no such service")
		return
	}
	if err != nil {
		h.logger.Error("failed to resolve service", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFRead, m.AccountID) {
		return
	}

	svc, err := h.serviceFromMapping(r, m)
	if errors.Is(err, bss.ErrACSUnreachable) {
		writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
		return
	}
	if err != nil {
		h.logger.Error("failed to build service resource", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, svc)
}

// listServices implements GET /service?accountId=.... accountId is required
// so the inventory can never degrade into an unscoped cross-tenant list.
func (h *handler) listServices(w http.ResponseWriter, r *http.Request) {
	accountID := r.URL.Query().Get("accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "accountId query parameter is required")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFRead, accountID) {
		return
	}

	mappings, err := h.mappings.ListByAccount(r.Context(), accountID)
	if err != nil {
		h.logger.Error("failed to list services", "err", err, "account_id", accountID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	svcs := make([]tmfService, 0, len(mappings))
	for i := range mappings {
		svc, err := h.serviceFromMapping(r, &mappings[i])
		if err != nil {
			h.logger.Error("failed to build service resource", "err", err, "id", mappings[i].ID)
			writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
			return
		}
		svcs = append(svcs, svc)
	}
	writeJSON(w, http.StatusOK, svcs)
}
