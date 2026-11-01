// TMF640 PATCH /service/{id} and GET /monitor/{id} (design
// docs/superpowers/specs/2026-09-13-bss-tmf640-design.md §4.2-4.3). A PATCH
// is interpreted into exactly one /bss/v1 action and goes through the same
// dispatchOrder outbox sequence as POST /bss/v1/orders; the Monitor is that
// order's state, read at request time.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"acs/internal/bss"
)

type tmfMonitorResponse struct {
	StatusCode string `json:"statusCode,omitempty"`
	Body       string `json:"body,omitempty"`
}

type tmfMonitor struct {
	ID         string              `json:"id"`
	Href       string              `json:"href"`
	State      string              `json:"state"` // InProgress | Completed | InError
	SourceHref string              `json:"sourceHref,omitempty"`
	Response   *tmfMonitorResponse `json:"response,omitempty"`
}

// tmf640Patch is the merge-patch body. Any other top-level field is
// refused, not ignored (§4.2: one recognised shape per PATCH).
type tmf640Patch struct {
	State                 *string                    `json:"state"`
	ServiceCharacteristic []tmfServiceCharacteristic `json:"serviceCharacteristic"`
}

var errTMF640Patch = errors.New("invalid TMF640 PATCH")

// interpretTMF640Patch turns a PATCH body into one /bss/v1 action and its
// parameters: state inactive/active is SUSPEND/ACTIVATE; SSID and
// WiFiPassword characteristics are MODIFY_WIFI.
func interpretTMF640Patch(raw []byte) (action string, params map[string]string, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p tmf640Patch
	if err := dec.Decode(&p); err != nil {
		return "", nil, errors.Join(errTMF640Patch, errors.New("the body may carry only state or serviceCharacteristic"))
	}
	switch {
	case p.State != nil && len(p.ServiceCharacteristic) > 0:
		return "", nil, errors.Join(errTMF640Patch, errors.New("a PATCH changes either state or serviceCharacteristic, not both"))
	case p.State != nil:
		switch *p.State {
		case "inactive":
			return "SUSPEND", nil, nil
		case "active":
			return "ACTIVATE", nil, nil
		}
		return "", nil, errors.Join(errTMF640Patch, errors.New(`state must be "active" or "inactive"`))
	case len(p.ServiceCharacteristic) > 0:
		params = map[string]string{}
		for _, c := range p.ServiceCharacteristic {
			switch c.Name {
			case "SSID":
				params["wifi_ssid"] = c.Value
			case "WiFiPassword":
				params["wifi_password"] = c.Value
			case "WiFiPasswordHandle":
				// A sealed handle from POST /bss/v1/secrets (I4).
				params["wifi_password_handle"] = c.Value
			default:
				return "", nil, errors.Join(errTMF640Patch, errors.New("unsupported serviceCharacteristic "+c.Name+"; SSID, WiFiPassword and WiFiPasswordHandle are supported"))
			}
		}
		return "MODIFY_WIFI", params, nil
	}
	return "", nil, errors.Join(errTMF640Patch, errors.New("the body carries no change"))
}

// patchService implements PATCH /service/{id}. X-Idempotency-Key is
// required and becomes the order's external_order_id, so a retried PATCH
// answers with the original Monitor and dispatches nothing more.
func (h *handler) patchService(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("X-Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "X-Idempotency-Key header is required")
		return
	}
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
	if !h.authorizeTMF(w, r, bss.ScopeTMFExecute, m.AccountID) {
		return
	}

	// A retry: same key, same service. A key reused for another service or
	// account is a caller bug, never a way to read someone else's order.
	if existing, err := h.mappings.FindOrder(r.Context(), key); err != nil {
		h.logger.Error("failed to check order idempotency", "err", err, "external_order_id", key)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	} else if existing != nil {
		if existing.AccountID != m.AccountID || existing.DeviceID != m.DeviceID {
			writeError(w, http.StatusConflict, "ErrIdempotencyKeyReused", "this X-Idempotency-Key was already used for another service")
			return
		}
		h.writeMonitor(w, r, http.StatusAccepted, existing, m.ID)
		return
	}

	if m.UnassignedAt != nil {
		writeError(w, http.StatusConflict, "ErrServiceTerminated", "the service has ended; its device is no longer assigned to the account")
		return
	}
	if m.Status == bss.StatusPendingActive {
		writeError(w, http.StatusConflict, "ErrServiceNotActive", "the service is still being onboarded; change it once it is active")
		return
	}

	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "invalid JSON body")
		return
	}
	action, params, err := interpretTMF640Patch(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", err.Error())
		return
	}
	writes, err := h.translateActionForDevice(r.Context(), action, params, m.DeviceID)
	switch {
	case errors.Is(err, bss.ErrACSUnreachable):
		writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
		return
	case errors.Is(err, bss.ErrDeviceLookupNotFound):
		writeError(w, http.StatusNotFound, "ErrDeviceNotFound", "the service's device no longer exists")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, "ErrUnsupportedAction", err.Error())
		return
	}
	if err := h.claimSecrets(r.Context(), r, params, writes, m.DeviceID, true); err != nil {
		if !writeSecretError(w, err) {
			h.logger.Error("failed to claim secret handles", "err", err, "external_order_id", key)
			writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		}
		return
	}

	_, err = h.dispatchOrder(r.Context(), key, m.AccountID, action, m.DeviceID, writes)
	if err != nil && !errors.Is(err, bss.ErrOrderAlreadyExists) && !errors.Is(err, bss.ErrACSUnreachable) {
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	// The order row exists whatever dispatch did: an unreachable ACS left it
	// PENDING_DISPATCH for the reconciler, which the Monitor reports as
	// InProgress; a lost race returns the winner's order.
	order, err := h.mappings.FindOrder(r.Context(), key)
	if err != nil || order == nil {
		h.logger.Error("failed to read order after TMF640 dispatch", "err", err, "external_order_id", key)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	h.writeMonitor(w, r, http.StatusAccepted, order, m.ID)
}

// getMonitor implements GET /monitor/{id}; id is the PATCH's
// X-Idempotency-Key. Only orders placed through the BSS adapter exist here.
func (h *handler) getMonitor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	order, err := h.mappings.FindOrder(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to read monitor", "err", err, "id", id)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	if order == nil {
		writeError(w, http.StatusNotFound, "ErrNotFound", "no such monitor")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFRead, order.AccountID) {
		return
	}
	serviceID := ""
	if m, err := h.mappings.CurrentMapping(r.Context(), order.AccountID, order.DeviceID); err == nil {
		serviceID = m.ID
	}
	h.writeMonitor(w, r, http.StatusOK, order, serviceID)
}

// writeMonitor reshapes a bss_orders row (plus its job once dispatched)
// into a Monitor (§4.3).
func (h *handler) writeMonitor(w http.ResponseWriter, r *http.Request, status int, order *bss.OrderRecord, serviceID string) {
	mon := tmfMonitor{ID: order.ExternalOrderID, Href: tmfMonitorBasePath + order.ExternalOrderID, State: "InProgress"}
	if serviceID != "" {
		mon.SourceHref = tmfServiceBasePath + serviceID
	}
	switch order.Status {
	case bss.OrderStatusDeadLettered:
		mon.State = "InError"
		mon.Response = &tmfMonitorResponse{StatusCode: "500", Body: firstNonEmpty(order.LastError, "the order could not be dispatched")}
	case bss.OrderStatusDispatched:
		job, err := h.acs.GetJobStatus(r.Context(), order.CommandKey)
		if errors.Is(err, bss.ErrACSUnreachable) {
			writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
			return
		}
		if err != nil {
			h.logger.Error("failed to read job for monitor", "err", err, "command_key", order.CommandKey)
			writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
			return
		}
		switch job.Status {
		case "SUCCESS":
			mon.State = "Completed"
			mon.Response = &tmfMonitorResponse{StatusCode: "200", Body: "applied and confirmed on the device"}
		case "FAILED", "TIMEOUT":
			body := job.Status
			if job.FaultString != nil && *job.FaultString != "" {
				body += ": " + *job.FaultString
			}
			mon.State = "InError"
			mon.Response = &tmfMonitorResponse{StatusCode: "500", Body: body}
		}
	}
	writeJSON(w, status, mon)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
