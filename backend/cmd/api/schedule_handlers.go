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
	CustomerID      *string         `json:"customer_id,omitempty"`
	CreatedAt       string          `json:"created_at"`
}

func toScheduledJobResponse(sj *scheduler.ScheduledJob) scheduledJobResponse {
	resp := scheduledJobResponse{
		ID: sj.ID, Name: sj.Name, JobType: sj.JobType, TargetType: sj.TargetType, TargetID: sj.TargetID,
		Payload: sj.Payload, IntervalSeconds: sj.IntervalSeconds, Enabled: sj.Enabled, CustomerID: sj.CustomerID,
		NextRunAt: sj.NextRunAt.Format(time.RFC3339), CreatedAt: sj.CreatedAt.Format(time.RFC3339),
	}
	if sj.LastRunAt != nil {
		s := sj.LastRunAt.Format(time.RFC3339)
		resp.LastRunAt = &s
	}
	return resp
}

// scopedScheduledJob loads a schedule and enforces the caller's tenancy
// scope (audit P0.6/H-3), same 404-not-403 reasoning as scopedGroup.
func (h *handler) scopedScheduledJob(w http.ResponseWriter, r *http.Request, id string) (*scheduler.ScheduledJob, bool) {
	sj, err := h.schedules.ByID(r.Context(), id)
	if errors.Is(err, scheduler.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		h.logger.Error("failed to get scheduled job", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if scoped && !deviceInScope(sj.CustomerID, customerIDs) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	return sj, true
}

type createScheduledJobRequest struct {
	Name            string          `json:"name"`
	JobType         string          `json:"job_type"`
	TargetType      string          `json:"target_type"`
	TargetID        string          `json:"target_id"`
	Payload         json.RawMessage `json:"payload"`
	IntervalSeconds int             `json:"interval_seconds"`
	CustomerID      *string         `json:"customer_id,omitempty"`
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

	// audit P0.6/H-3: a schedule fires unattended, long after this
	// request, so its target authorization has to be persisted now
	// (checked again at fire time in schedule_worker.go, since the
	// target's own customer can drift between now and then). A scoped
	// operator must name a customer_id within scope, and the concrete
	// target (device or group) must belong to that same customer —
	// otherwise a scoped operator could schedule a recurring RPC against
	// any device UUID or group in the platform, not just their own.
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if scoped {
		if req.CustomerID == nil || !deviceInScope(req.CustomerID, customerIDs) {
			http.Error(w, "customer_id is required and must be within your assigned scope", http.StatusBadRequest)
			return
		}
	}
	switch req.TargetType {
	case scheduler.TargetDevice:
		d, err := h.devices.Get(r.Context(), req.TargetID)
		if err != nil || (scoped && !deviceInScope(d.CustomerID, customerIDs)) || !sameCustomer(d.CustomerID, req.CustomerID) {
			http.Error(w, "target device not found or outside customer_id", http.StatusBadRequest)
			return
		}
	case scheduler.TargetGroup:
		g, err := h.groups.Get(r.Context(), req.TargetID)
		if err != nil || (scoped && !deviceInScope(g.CustomerID, customerIDs)) || !sameCustomer(g.CustomerID, req.CustomerID) {
			http.Error(w, "target group not found or outside customer_id", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, `target_type must be "DEVICE" or "GROUP"`, http.StatusBadRequest)
		return
	}

	sj, err := h.schedules.Create(r.Context(), req.Name, req.JobType, req.TargetType, req.TargetID,
		json.RawMessage(req.Payload), req.IntervalSeconds, req.CustomerID, operatorFromRequest(r))
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
		"scheduled_job_id": sj.ID, "name": sj.Name, "job_type": sj.JobType, "customer_id": sj.CustomerID,
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
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]scheduledJobResponse, 0, len(list))
	for _, sj := range list {
		if scoped && !deviceInScope(sj.CustomerID, customerIDs) {
			continue
		}
		items = append(items, toScheduledJobResponse(&sj))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) deleteScheduledJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.scopedScheduledJob(w, r, id); !ok {
		return
	}
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
		if _, ok := h.scopedScheduledJob(w, r, id); !ok {
			return
		}
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
