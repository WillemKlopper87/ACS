// TMF640 (Service Activation and Configuration) shapes, sub-project C-2
// (design docs/superpowers/specs/2026-09-13-bss-tmf640-design.md). A thin
// translation layer over the same account_device_mappings/bss_orders
// data and dispatchOrder sequence /bss/v1/* already uses -- no new
// dispatch logic lives here.
//
// TMF640 is CRUD on a Service resource plus a Monitor resource for async
// tracking -- verified against the real TM Forum swagger spec before
// this was written, not assumed from the loose "ServiceOrder" shape an
// earlier architecture note incorrectly described (that's TMF641, a
// different API -- see design S2).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
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

// tmfService is TMF640's Service resource, reduced to the fields this
// increment populates (design §4.1) -- every other TMF640 Service field
// (feature, serviceRelationship, supportingResource, ...) is valid to
// omit per TMF's own extensibility model, not an error.
type tmfService struct {
	ID                    string                     `json:"id"`
	Href                  string                     `json:"href"`
	Category              string                     `json:"category"`
	State                 string                     `json:"state"`
	ServiceCharacteristic []tmfServiceCharacteristic `json:"serviceCharacteristic"`
	RelatedParty          []tmfRelatedParty          `json:"relatedParty"`
}

// serviceFromMapping builds a Service resource for one mapping, resolving
// SSID's current value via ACSClient.GetParameters (never
// internal/parameters directly -- design S4.1's process-boundary rule)
// using the exact same canonical-parameter resolution
// internal/bss/template.go's translateModifyWifi already uses for the
// write side, so read and write can never disagree about which
// TR-181/TR-098 path a characteristic means. A characteristic whose
// value isn't in the cache yet (device never reported it) is simply
// omitted from ServiceCharacteristic, not an error -- a fresh device's
// Service is still a valid, mostly-empty read.
//
// WiFiPassword is deliberately never read back here: a security review
// flagged the original design (reflecting its live value in GET, per the
// spec as first written) as exposing a plaintext credential to any
// authenticated BSS integrator. Redacting it in reads -- and never even
// requesting its value from ACSClient.GetParameters, so the plaintext
// doesn't transit this path at all -- was the resulting decision;
// PATCH /service/{id} (a later task) can still write it, matching common
// TR-069/TR-369 practice of treating KeyPassphrase as write-only.
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

// listServices implements GET /service?accountId=.... TMF640's own base
// spec has no account-scoping query param (multi-tenant scoping is an
// implementation concern, not part of the standard) -- accountId is
// required here because bssadapter has no "list every account's
// services" operation, mirroring /bss/v1/mappings/{account_id}'s own
// account-scoped-only design.
func (h *handler) listServices(w http.ResponseWriter, r *http.Request) {
	accountID := r.URL.Query().Get("accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "accountId query parameter is required")
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

type tmfMonitorResponse struct {
	StatusCode string `json:"statusCode"`
	Body       string `json:"body"`
}

// tmfMonitor is TMF640's Monitor resource, reduced to the fields this
// increment populates (design §4.3).
type tmfMonitor struct {
	ID         string              `json:"id"`
	Href       string              `json:"href"`
	SourceHref string              `json:"sourceHref,omitempty"`
	State      string              `json:"state"`
	Response   *tmfMonitorResponse `json:"response,omitempty"`
}

// monitorFromOrder reshapes bss_orders' existing status (C-1) into
// TMF640's InProgress/Completed/InError envelope (design §4.3) -- the
// one place this mapping exists, shared between GET /monitor/{id} and
// PATCH /service/{id}'s response body (TMF640's own "server returns
// Monitor as POST/PATCH response" pattern, added in the next task) so
// the two can never disagree about what "done" means. serviceID is the
// mapping id to link back to via sourceHref -- empty when called from
// GET /monitor/{id} directly (no service context available there without
// an extra lookup this increment doesn't need), populated when called
// from PATCH, which already has it from its own URL path.
func (h *handler) monitorFromOrder(r *http.Request, externalOrderID, serviceID string, order *bss.OrderRecord) tmfMonitor {
	m := tmfMonitor{
		ID:   externalOrderID,
		Href: tmfMonitorBasePath + externalOrderID,
	}
	if serviceID != "" {
		m.SourceHref = tmfServiceBasePath + serviceID
	}

	switch order.Status {
	case bss.OrderStatusPendingDispatch:
		m.State = "InProgress"
	case bss.OrderStatusDeadLettered:
		m.State = "InError"
		m.Response = &tmfMonitorResponse{StatusCode: "500", Body: order.LastError}
	case bss.OrderStatusDispatched:
		status, err := h.acs.GetJobStatus(r.Context(), order.CommandKey)
		if err != nil {
			// Can't confirm a terminal state right now (a transient ACS
			// blip); a later poll will resolve it -- report InProgress
			// rather than surfacing an unreachable-ACS error for a pure
			// status read.
			m.State = "InProgress"
			return m
		}
		switch status.Status {
		case "SUCCESS":
			m.State = "Completed"
			m.Response = &tmfMonitorResponse{StatusCode: "200", Body: "completed"}
		case "FAILED", "TIMEOUT":
			m.State = "InError"
			fault := ""
			if status.FaultString != nil {
				fault = *status.FaultString
			}
			m.Response = &tmfMonitorResponse{StatusCode: "500", Body: fault}
		default:
			m.State = "InProgress"
		}
	}
	return m
}

// getMonitor implements GET /monitor/{id}. id is the same
// external_order_id a PATCH /service/{id} caller supplied as
// X-Idempotency-Key (added in the next task).
func (h *handler) getMonitor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	order, err := h.mappings.FindOrder(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to look up order for monitor", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	if order == nil {
		writeError(w, http.StatusNotFound, "ErrNotFound", "no such monitor")
		return
	}
	writeJSON(w, http.StatusOK, h.monitorFromOrder(r, id, "", order))
}

type tmfServicePatch struct {
	State                 *string                    `json:"state,omitempty"`
	ServiceCharacteristic []tmfServiceCharacteristic `json:"serviceCharacteristic,omitempty"`
}

// interpretServicePatch turns a TMF640 JSON-merge-patch body into
// exactly one of the three actions internal/bss/template.go's registry
// already knows (design §4.2) -- one recognized shape per PATCH, mixing
// or naming anything else is rejected.
func interpretServicePatch(patch tmfServicePatch) (action string, params map[string]string, err error) {
	hasState := patch.State != nil
	hasChar := len(patch.ServiceCharacteristic) > 0

	if hasState && hasChar {
		return "", nil, errors.New("a PATCH may change either state or serviceCharacteristic, not both")
	}
	if hasState {
		switch *patch.State {
		case "active":
			return "ACTIVATE", nil, nil
		case "inactive":
			return "SUSPEND", nil, nil
		default:
			return "", nil, fmt.Errorf("unsupported state %q: only \"active\" and \"inactive\" are supported", *patch.State)
		}
	}
	if hasChar {
		params = map[string]string{}
		for _, c := range patch.ServiceCharacteristic {
			switch c.Name {
			case "SSID":
				params["wifi_ssid"] = c.Value
			case "WiFiPassword":
				params["wifi_password"] = c.Value
			default:
				return "", nil, fmt.Errorf("unsupported serviceCharacteristic %q: only \"SSID\" and \"WiFiPassword\" are supported", c.Name)
			}
		}
		return "MODIFY_WIFI", params, nil
	}
	return "", nil, errors.New("PATCH body must set either state or serviceCharacteristic")
}

// patchService implements PATCH /service/{id} -- TMF640's activation/
// configuration entry point (design §4.2). X-Idempotency-Key maps onto
// external_order_id; without it, an ordinary network-level retry of this
// PATCH would double-dispatch, reopening the exact risk C-1's outbox
// exists to close for /bss/v1/orders. The interpreted action is handed
// to dispatchOrder (Task 3) -- the exact same write-ahead-then-dispatch
// sequence /bss/v1/orders uses, not a parallel copy.
func (h *handler) patchService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	idempotencyKey := r.Header.Get("X-Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "X-Idempotency-Key header is required")
		return
	}

	var patch tmfServicePatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "invalid JSON body")
		return
	}
	action, params, err := interpretServicePatch(patch)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", err.Error())
		return
	}

	if existing, err := h.mappings.FindOrder(r.Context(), idempotencyKey); err != nil {
		h.logger.Error("failed to check order idempotency", "err", err, "external_order_id", idempotencyKey)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	} else if existing != nil {
		writeJSON(w, http.StatusAccepted, h.monitorFromOrder(r, idempotencyKey, id, existing))
		return
	}

	mapping, err := h.mappings.GetMappingByID(r.Context(), id)
	if errors.Is(err, bss.ErrMappingNotFound) {
		writeError(w, http.StatusNotFound, "ErrNotFound", "no such service")
		return
	}
	if err != nil {
		h.logger.Error("failed to resolve service for patch", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	dataModelRoot := ""
	if action == "MODIFY_WIFI" {
		dev, err := h.acs.GetDevice(r.Context(), mapping.DeviceID)
		if errors.Is(err, bss.ErrACSUnreachable) {
			writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
			return
		}
		if err != nil {
			h.logger.Error("failed to resolve device for patch translation", "err", err, "device_id", mapping.DeviceID)
			writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
			return
		}
		dataModelRoot = dev.DataModelRoot
	}

	translated, err := bss.Translate(action, params, h.walledGarden, dataModelRoot)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", err.Error())
		return
	}

	if _, err := h.dispatchOrder(r.Context(), idempotencyKey, mapping.AccountID, action, mapping.DeviceID, translated); err != nil {
		if errors.Is(err, bss.ErrOrderAlreadyExists) {
			existing, findErr := h.mappings.FindOrder(r.Context(), idempotencyKey)
			if findErr != nil || existing == nil {
				h.logger.Error("failed to resolve order after InsertPending race", "err", findErr, "external_order_id", idempotencyKey)
				writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
				return
			}
			writeJSON(w, http.StatusAccepted, h.monitorFromOrder(r, idempotencyKey, id, existing))
			return
		}
		if errors.Is(err, bss.ErrACSUnreachable) {
			writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
			return
		}
		h.logger.Error("failed to dispatch service patch to ACS", "err", err, "id", id, "action", action)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	order, err := h.mappings.FindOrder(r.Context(), idempotencyKey)
	if err != nil || order == nil {
		h.logger.Error("failed to re-read order after dispatch", "err", err, "external_order_id", idempotencyKey)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	writeJSON(w, http.StatusAccepted, h.monitorFromOrder(r, idempotencyKey, id, order))
}
