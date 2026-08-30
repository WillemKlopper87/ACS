// Job read handlers and the request-actor helper (split out of main.go,
// audit P3.1).
package main

import (
	"acs/internal/jobs"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// jobResponse mirrors design doc v3 §8.5's job status shape.
type jobResponse struct {
	CommandKey   string          `json:"command_key"`
	DeviceID     string          `json:"device_id"`
	Type         string          `json:"type"`
	Status       string          `json:"status"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	CompletedAt  *string         `json:"completed_at,omitempty"`
	FaultCode    *string         `json:"fault_code,omitempty"`
	FaultString  *string         `json:"fault_string,omitempty"`
	ResultDetail json.RawMessage `json:"result_detail,omitempty"`
}

func toJobResponse(job *jobs.Job) jobResponse {
	resp := jobResponse{
		CommandKey:   job.CommandKey,
		DeviceID:     job.DeviceID,
		Type:         job.Type,
		Status:       job.Status,
		CreatedAt:    job.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    job.UpdatedAt.Format(time.RFC3339),
		FaultCode:    job.FaultCode,
		FaultString:  job.FaultString,
		ResultDetail: job.ResultDetail,
	}
	if job.CompletedAt != nil {
		s := job.CompletedAt.Format(time.RFC3339)
		resp.CompletedAt = &s
	}
	return resp
}

// listJobs backs the Jobs screen — every job across the fleet, most
// recent first, capped at jobs.listLimit since there's no pagination yet.
func (h *handler) listJobs(w http.ResponseWriter, r *http.Request) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	list, err := h.jobs.List(r.Context(), "", customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to list jobs", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]jobResponse, 0, len(list))
	for i := range list {
		items = append(items, toJobResponse(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// listDeviceJobs backs Device Detail's recent-activity panel — one
// device's jobs, most recent first.
func (h *handler) listDeviceJobs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	list, err := h.jobs.List(r.Context(), id, nil, false)
	if err != nil {
		h.logger.Error("failed to list device jobs", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]jobResponse, 0, len(list))
	for i := range list {
		items = append(items, toJobResponse(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (h *handler) getJob(w http.ResponseWriter, r *http.Request) {
	commandKey := r.PathValue("command_key")
	job, err := h.jobs.ByCommandKey(r.Context(), commandKey)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to get job", "err", err, "command_key", commandKey)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A job belongs to a device; the caller must be in that device's
	// tenancy scope (audit P0.2) — same 404-not-403 shape as devices.
	if _, ok := h.getScopedDevice(w, r, job.DeviceID); !ok {
		return
	}

	writeJSON(w, http.StatusOK, toJobResponse(job))
}

// operatorFromRequest reports who's making this request, for the audit
// trail and jobs.created_by. When JWT auth is enabled (ACS_JWT_SIGNING_SECRET
// set), this is the authenticated operator's username, put in context by
// withJWTAuth. When auth is disabled (lab mode — see that middleware),
// there's no real identity to report, so it falls back to the same
// generic "operator" Phase 2-5 used.
func operatorFromRequest(r *http.Request) string {
	if claims, ok := operatorClaims(r.Context()); ok {
		return claims.Subject
	}
	return "operator"
}
