// Device console/REPL backing endpoint (admin-platform backlog: "console/
// REPL screen interface"). The console screen itself is just a thin
// command parser over the job types this ACS already has — GET_PARAMETER
// was the one gap: every other verb (set, reboot, ping, traceroute,
// addobject, discover, factory-reset) already has a REST endpoint from
// earlier feature work, but there was no way to trigger a live
// GetParameterValues for arbitrary paths outside the two hardcoded
// refresh-cellular/refresh-wifi-clients callers. This fills that gap
// generically.
package main

import (
	"encoding/json"
	"net/http"

	"acs/internal/jobs"
)

type getParametersLiveRequest struct {
	Paths []string `json:"paths"`
}

// createGetParametersLive queues a GET_PARAMETER job for arbitrary paths —
// the "get <path>" console command, and the generic form refresh-cellular/
// refresh-wifi-clients are specific instances of.
func (h *handler) createGetParametersLive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	var req getParametersLiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.Paths) == 0 {
		http.Error(w, "paths is required and must be non-empty", http.StatusBadRequest)
		return
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: req.Paths}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue live parameter read", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"command_key": job.CommandKey, "status": job.Status})
}
