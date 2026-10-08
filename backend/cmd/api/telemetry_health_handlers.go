package main

import (
	"acs/internal/devices/adapters"
	"acs/internal/jobs"
	"acs/internal/transfer"
	"net/http"
	"strconv"
	"time"
)

func (h *handler) renewBulkData(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if len(h.transferKey) == 0 {
		http.Error(w, "transfer signing key unavailable", 503)
		return
	}
	receiver, err := h.bulkData.Get(r.Context(), r.PathValue("receiver_id"))
	if err != nil || receiver.DeviceID != device.ID {
		http.Error(w, "receiver not found", 404)
		return
	}
	if receiver.Revoked {
		http.Error(w, "revoked receivers cannot be renewed", 409)
		return
	}
	expires := time.Now().UTC().Add(7 * 24 * time.Hour)
	token := transfer.Sign(h.transferKey, "bulk-data", receiver.ID, expires)
	payload := jobs.SetParameterPayload{Parameters: []jobs.ParameterWrite{{Name: "Device.BulkData.Profile." + strconv.Itoa(receiver.ProfileInstance) + ".HTTP.Password", Value: token, Type: "xsd:string"}}}
	if err := h.validateWritableParameters(r.Context(), device.ID, payload); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	key, err := h.bulkData.RenewAndQueue(r.Context(), receiver, expires, payload, operatorFromRequest(r))
	if err != nil {
		http.Error(w, "failed to renew receiver", 500)
		return
	}
	writeJSON(w, 202, map[string]any{"command_key": key, "expires_at": expires, "status": "QUEUED"})
}
func (h *handler) getTelemetryHealth(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	health, err := h.bulkData.Health(r.Context(), device.ID, time.Now())
	if err != nil {
		http.Error(w, "failed to read telemetry health", 500)
		return
	}
	writeJSON(w, 200, map[string]any{"receivers": health, "as_of": time.Now().UTC()})
}
func (h *handler) getFiveG(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	dictionary, err := dictionaryForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	cache, err := h.params.Get(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read telemetry", 500)
		return
	}
	names, err := h.params.GetNames(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read capabilities", 500)
		return
	}
	discovered := map[string]bool{}
	if names != nil {
		discovered = names.Names
	}
	writeJSON(w, 200, map[string]any{"metrics": adapters.ProjectFiveG(dictionary, discovered, cache, time.Now(), 5*time.Minute), "as_of": time.Now().UTC(), "freshness_seconds": 300, "scope": "discovered standard and vendor paths; raw vendor units are not inferred"})
}
