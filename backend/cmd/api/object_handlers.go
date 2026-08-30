// AddObject/DeleteObject/Reboot/FactoryReset (critical feature backlog:
// the biggest protocol-completeness gap this build had against an
// off-the-shelf ACS — every prior write path could only edit parameters
// that already existed on a device; these four close that).
package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"acs/internal/jobs"
)

type addObjectRequest struct {
	ObjectPath string `json:"object_path"`
}

// createAddObject queues an ADD_OBJECT job. object_path must end in "."
// (TR-069's own convention for "this names a container, not a leaf") —
// rejected early rather than sent to the CPE and faulted there.
func (h *handler) createAddObject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	var req addObjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.ObjectPath == "" || !strings.HasSuffix(req.ObjectPath, ".") {
		http.Error(w, "object_path is required and must end in \".\" (e.g. \"Device.WiFi.SSID.\")", http.StatusBadRequest)
		return
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeAddObject, jobs.AddObjectPayload{ObjectPath: req.ObjectPath}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue add object", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"command_key": job.CommandKey, "status": job.Status})
}

type deleteObjectRequest struct {
	ObjectPath string `json:"object_path"`
}

// createDeleteObject queues a DELETE_OBJECT job. object_path must name a
// specific instance (ending in "." after the instance number, e.g.
// "Device.WiFi.SSID.3."), not the parent container.
func (h *handler) createDeleteObject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	var req deleteObjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.ObjectPath == "" || !strings.HasSuffix(req.ObjectPath, ".") {
		http.Error(w, "object_path is required and must end in \".\" (e.g. \"Device.WiFi.SSID.3.\")", http.StatusBadRequest)
		return
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeDeleteObject, jobs.DeleteObjectPayload{ObjectPath: req.ObjectPath}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue delete object", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"command_key": job.CommandKey, "status": job.Status})
}

// createReboot queues a REBOOT job — the CPE drops the connection and
// restarts; the real confirmation is a fresh Inform with event code
// "M Reboot", not anything this response can carry.
func (h *handler) createReboot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeReboot, jobs.RebootPayload{}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue reboot", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), id, "RebootQueued", map[string]any{"command_key": job.CommandKey}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"command_key": job.CommandKey, "status": job.Status})
}

// createFactoryReset queues a FACTORY_RESET job — audited distinctly
// (RebootQueued vs FactoryResetQueued) since this one is destructive and
// an operator reviewing the audit log needs to be able to tell them apart
// at a glance, not just by clicking into details.
func (h *handler) createFactoryReset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeFactoryReset, jobs.FactoryResetPayload{}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue factory reset", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), id, "FactoryResetQueued", map[string]any{"command_key": job.CommandKey}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"command_key": job.CommandKey, "status": job.Status})
}
