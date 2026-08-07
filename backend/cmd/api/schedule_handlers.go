// Scheduled jobs REST surface (build plan §4 Phase 7).
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"acs/internal/scheduler"
)

type scheduledJobResponse struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	JobType         string          `json:"job_type"`
	TargetType      string          `json:"target_type"`
	TargetID        string          `json:"target_id"`
	Payload         json.RawMessage `json:"payload"`
	IntervalSeconds int             `json:"interval_seconds"`
	Enabled         bool            `json:"enabled"`
	NextRunAt       string          `json:"next_run_at"`
	LastRunAt       *string         `json:"last_run_at,omitempty"`
	CreatedAt       string          `json:"created_at"`
}

func toScheduledJobResponse(sj *scheduler.ScheduledJob) scheduledJobResponse {
	resp := scheduledJobResponse{
		ID: sj.ID, Name: sj.Name, JobType: sj.JobType, TargetType: sj.TargetType, TargetID: sj.TargetID,
		Payload: sj.Payload, IntervalSeconds: sj.IntervalSeconds, Enabled: sj.Enabled,
		NextRunAt: sj.NextRunAt.Format(time.RFC3339), CreatedAt: sj.CreatedAt.Format(time.RFC3339),
	}
	if sj.LastRunAt != nil {
		s := sj.LastRunAt.Format(time.RFC3339)
		resp.LastRunAt = &s
	}
	return resp
}

type createScheduledJobRequest struct {
	Name            string          `json:"name"`
	JobType         string          `json:"job_type"`
	TargetType      string          `json:"target_type"`
	TargetID        string          `json:"target_id"`
	Payload         json.RawMessage `json:"payload"`
	IntervalSeconds int             `json:"interval_seconds"`
}

func (h *handler) createScheduledJob(w http.ResponseWriter, r *http.Request) {
	var req createScheduledJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.JobType == "" || req.TargetType == "" || req.TargetID == "" {
		http.Error(w, "name, job_type, target_type, and target_id are required", http.StatusBadRequest)
		return
	}

	sj, err := h.schedules.Create(r.Context(), req.Name, req.JobType, req.TargetType, req.TargetID,
		json.RawMessage(req.Payload), req.IntervalSeconds, operatorFromRequest(r))
	if errors.Is(err, scheduler.ErrIntervalTooShort) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		h.logger.Error("failed to create scheduled job", "err", err, "name", req.Name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "ScheduledJobCreated", map[string]any{
		"scheduled_job_id": sj.ID, "name": sj.Name, "job_type": sj.JobType,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	writeJSON(w, http.StatusCreated, toScheduledJobResponse(sj))
}

func (h *handler) listScheduledJobs(w http.ResponseWriter, r *http.Request) {
	list, err := h.schedules.List(r.Context())
	if err != nil {
		h.logger.Error("failed to list scheduled jobs", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]scheduledJobResponse, 0, len(list))
	for _, sj := range list {
		items = append(items, toScheduledJobResponse(&sj))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) deleteScheduledJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.schedules.Delete(r.Context(), id); errors.Is(err, scheduler.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to delete scheduled job", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "ScheduledJobDeleted", map[string]any{
		"scheduled_job_id": id,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) setScheduledJobEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		sj, err := h.schedules.SetEnabled(r.Context(), id, enabled)
		if errors.Is(err, scheduler.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			h.logger.Error("failed to update scheduled job", "err", err, "id", id, "enabled", enabled)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, toScheduledJobResponse(sj))
	}
}
