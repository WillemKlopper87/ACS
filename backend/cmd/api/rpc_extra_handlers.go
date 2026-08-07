// ScheduleInform, SetParameterAttributes/GetParameterAttributes — nice-to-
// have feature backlog.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"acs/internal/jobs"
)

type scheduleInformRequest struct {
	DelaySeconds int `json:"delay_seconds"`
}

// createScheduleInform queues a SCHEDULE_INFORM job — tells a CPE to
// Inform again after a delay, independent of its periodic interval or an
// ACS-initiated Connection Request.
func (h *handler) createScheduleInform(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.devices.Get(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to get device", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var req scheduleInformRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.DelaySeconds <= 0 {
		http.Error(w, "delay_seconds must be positive", http.StatusBadRequest)
		return
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeScheduleInform, jobs.ScheduleInformPayload{DelaySeconds: req.DelaySeconds}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue schedule inform", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"command_key": job.CommandKey, "status": job.Status})
}

type setParameterAttributesRequest struct {
	Attributes []struct {
		Name         string `json:"name"`
		Notification int    `json:"notification"`
	} `json:"attributes"`
}

// createSetParameterAttributes queues a SET_PARAMETER_ATTRIBUTES job —
// configures whether the CPE should actively Inform on a parameter's
// change (Notification 2), passively report it on the next Inform anyway
// (1), or neither (0), rather than the ACS having to poll for it.
func (h *handler) createSetParameterAttributes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.devices.Get(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to get device", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var req setParameterAttributesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.Attributes) == 0 {
		http.Error(w, "at least one attribute is required", http.StatusBadRequest)
		return
	}
	attrs := make([]jobs.AttributeWrite, len(req.Attributes))
	for i, a := range req.Attributes {
		if a.Name == "" || a.Notification < 0 || a.Notification > 2 {
			http.Error(w, "each attribute needs a name and notification in [0,2]", http.StatusBadRequest)
			return
		}
		attrs[i] = jobs.AttributeWrite{Name: a.Name, Notification: a.Notification}
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeSetParameterAttributes, jobs.SetParameterAttributesPayload{Attributes: attrs}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue set parameter attributes", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"command_key": job.CommandKey, "status": job.Status})
}

type getParameterAttributesRequest struct {
	Paths []string `json:"paths"`
}

// createGetParameterAttributes queues a GET_PARAMETER_ATTRIBUTES job. The
// result (each path's current notification level) lands in the job's
// result_detail once it completes — poll GET /jobs/{command_key}, same as
// every other job type.
func (h *handler) createGetParameterAttributes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.devices.Get(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to get device", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var req getParameterAttributesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.Paths) == 0 {
		http.Error(w, "at least one path is required", http.StatusBadRequest)
		return
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeGetParameterAttributes, jobs.GetParameterAttributesPayload{Paths: req.Paths}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue get parameter attributes", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"command_key": job.CommandKey, "status": job.Status})
}
