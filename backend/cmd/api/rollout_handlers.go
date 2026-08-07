// Firmware canary rollouts (build plan §4 Phase 7 / design doc v3 §9.5,
// deferred from Phase 4's MVP scope).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"acs/internal/rollout"
)

type rolloutResponse struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	FirmwareImageID      string  `json:"firmware_image_id"`
	RollbackImageID      *string `json:"rollback_firmware_image_id,omitempty"`
	ModelFilter          *string `json:"model_filter,omitempty"`
	CurrentVersionFilter *string `json:"current_version_filter,omitempty"`
	CanaryPercentage     int     `json:"canary_percentage"`
	MaximumFailureRate   float64 `json:"maximum_failure_rate"`
	Status               string  `json:"status"`
	CreatedAt            string  `json:"created_at"`
	RollbackDispatchedAt *string `json:"rollback_dispatched_at,omitempty"`
}

func toRolloutResponse(ro *rollout.Rollout) rolloutResponse {
	resp := rolloutResponse{
		ID: ro.ID, Name: ro.Name, FirmwareImageID: ro.FirmwareImageID, RollbackImageID: ro.RollbackFirmwareImageID,
		ModelFilter: ro.ModelFilter, CurrentVersionFilter: ro.CurrentVersionFilter,
		CanaryPercentage: ro.CanaryPercentage, MaximumFailureRate: ro.MaximumFailureRate,
		Status: ro.Status, CreatedAt: ro.CreatedAt.Format(time.RFC3339),
	}
	if ro.RollbackDispatchedAt != nil {
		s := ro.RollbackDispatchedAt.Format(time.RFC3339)
		resp.RollbackDispatchedAt = &s
	}
	return resp
}

type createRolloutRequest struct {
	Name                    string  `json:"name"`
	FirmwareImageID         string  `json:"firmware_image_id"`
	RollbackFirmwareImageID *string `json:"rollback_firmware_image_id,omitempty"`
	ModelFilter             *string `json:"model_filter,omitempty"`
	CurrentVersionFilter    *string `json:"current_version_filter,omitempty"`
	CanaryPercentage        int     `json:"canary_percentage,omitempty"`
	MaximumFailureRate      float64 `json:"maximum_failure_rate,omitempty"`
	MaintenanceWindowStart  *string `json:"maintenance_window_start_utc,omitempty"` // "HH:MM:SS"
	MaintenanceWindowEnd    *string `json:"maintenance_window_end_utc,omitempty"`
}

func (h *handler) createRollout(w http.ResponseWriter, r *http.Request) {
	var req createRolloutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.FirmwareImageID == "" {
		http.Error(w, "name and firmware_image_id are required", http.StatusBadRequest)
		return
	}
	if req.CanaryPercentage == 0 {
		req.CanaryPercentage = 10
	}
	if req.MaximumFailureRate == 0 {
		req.MaximumFailureRate = 0.2
	}

	ro, err := h.rollouts.Create(r.Context(), req.Name, req.FirmwareImageID, req.RollbackFirmwareImageID,
		req.ModelFilter, req.CurrentVersionFilter, req.CanaryPercentage, req.MaximumFailureRate,
		req.MaintenanceWindowStart, req.MaintenanceWindowEnd, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to create rollout", "err", err, "name", req.Name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	eligible, err := h.rollouts.EligibleDeviceIDs(r.Context(), ro.ID)
	if err != nil {
		h.logger.Error("failed to count eligible rollout devices", "err", err, "rollout_id", ro.ID)
	}

	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), "", "RolloutCreated", map[string]any{
		"rollout_id": ro.ID, "name": ro.Name, "eligible_devices": len(eligible),
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	resp := toRolloutResponse(ro)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": resp.ID, "name": resp.Name, "firmware_image_id": resp.FirmwareImageID,
		"model_filter": resp.ModelFilter, "current_version_filter": resp.CurrentVersionFilter,
		"canary_percentage": resp.CanaryPercentage, "maximum_failure_rate": resp.MaximumFailureRate,
		"status": resp.Status, "created_at": resp.CreatedAt, "eligible_devices": len(eligible),
	})
}

func (h *handler) listRollouts(w http.ResponseWriter, r *http.Request) {
	list, err := h.rollouts.List(r.Context())
	if err != nil {
		h.logger.Error("failed to list rollouts", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]rolloutResponse, 0, len(list))
	for _, ro := range list {
		items = append(items, toRolloutResponse(&ro))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type rolloutDeviceResponse struct {
	DeviceID   string  `json:"device_id"`
	OUISerial  string  `json:"oui_serial"`
	State      string  `json:"state"`
	CommandKey *string `json:"command_key,omitempty"`
}

func (h *handler) getRollout(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ro, err := h.rollouts.ByID(r.Context(), id)
	if errors.Is(err, rollout.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to get rollout", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	statuses, err := h.rollouts.DeviceStatuses(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to get rollout device statuses", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rate, terminal, err := h.rollouts.FailureRate(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to compute rollout failure rate", "err", err, "id", id)
	}

	devices := make([]rolloutDeviceResponse, 0, len(statuses))
	counts := map[string]int{}
	for _, s := range statuses {
		devices = append(devices, rolloutDeviceResponse{DeviceID: s.DeviceID, OUISerial: s.OUISerial, State: s.State, CommandKey: s.CommandKey})
		counts[s.State]++
	}

	resp := toRolloutResponse(ro)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": resp.ID, "name": resp.Name, "firmware_image_id": resp.FirmwareImageID,
		"rollback_firmware_image_id": resp.RollbackImageID, "rollback_dispatched_at": resp.RollbackDispatchedAt,
		"canary_percentage": resp.CanaryPercentage, "maximum_failure_rate": resp.MaximumFailureRate,
		"status": resp.Status, "created_at": resp.CreatedAt,
		"devices": devices, "state_counts": counts,
		"failure_rate": math.Round(rate*1000) / 1000, "terminal_count": terminal,
	})
}

// startRollout dispatches the canary batch: canary_percentage% of
// eligible devices, minimum 1 (design doc v3 §9.5's canary_percentage
// control). Refuses outside the configured maintenance window rather
// than silently ignoring it.
func (h *handler) startRollout(w http.ResponseWriter, r *http.Request) {
	h.dispatchRolloutBatch(w, r, true)
}

// advanceRollout dispatches one more wave — another canary_percentage
// slice of the rollout's original eligible pool, same size as the canary
// batch that started it (build plan §4 Phase 4 firm-up: this used to
// dispatch everything remaining in one shot, a single "advance to
// everyone else" step; multiple calls now walk the fleet in waves of the
// configured size, each still gated on the accumulated failure rate,
// until nothing's left ELIGIBLE). Blocked (not advanced) if the failure
// rate among devices dispatched so far exceeds maximum_failure_rate —
// design doc v3 §9.5's control, actually enforced.
func (h *handler) advanceRollout(w http.ResponseWriter, r *http.Request) {
	h.dispatchRolloutBatch(w, r, false)
}

func (h *handler) dispatchRolloutBatch(w http.ResponseWriter, r *http.Request, isCanaryStart bool) {
	id := r.PathValue("id")
	ro, err := h.rollouts.ByID(r.Context(), id)
	if errors.Is(err, rollout.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to get rollout", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if isCanaryStart && ro.Status != rollout.StatusDraft {
		http.Error(w, rollout.ErrNotDraft.Error(), http.StatusConflict)
		return
	}
	if !isCanaryStart && ro.Status != rollout.StatusActive {
		http.Error(w, rollout.ErrNotActive.Error(), http.StatusConflict)
		return
	}

	if !rollout.InMaintenanceWindow(time.Now(), ro.MaintenanceWindowStartUTC, ro.MaintenanceWindowEndUTC) {
		http.Error(w, rollout.ErrOutsideMaintenance.Error(), http.StatusConflict)
		return
	}

	if !isCanaryStart {
		rate, terminal, err := h.rollouts.FailureRate(r.Context(), id)
		if err != nil {
			h.logger.Error("failed to compute rollout failure rate", "err", err, "id", id)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if terminal > 0 && rate > ro.MaximumFailureRate {
			_ = h.rollouts.SetStatus(r.Context(), id, rollout.StatusBlocked)
			operator := operatorFromRequest(r)
			if err := h.auditor.Record(r.Context(), operator, "", "RolloutBlocked", map[string]any{
				"rollout_id": id, "failure_rate": rate, "maximum_failure_rate": ro.MaximumFailureRate,
			}); err != nil {
				h.logger.Error("failed to write audit record", "err", err)
			}
			h.dispatchRollback(r.Context(), ro, operator)
			http.Error(w, rollout.ErrFailureRateExceeded.Error(), http.StatusConflict)
			return
		}
	}

	eligible, err := h.rollouts.EligibleDeviceIDs(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to list eligible rollout devices", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if len(eligible) == 0 {
		if isCanaryStart {
			http.Error(w, rollout.ErrNoEligibleDevices.Error(), http.StatusBadRequest)
			return
		}
		_ = h.rollouts.SetStatus(r.Context(), id, rollout.StatusCompleted)
		writeJSON(w, http.StatusOK, map[string]any{"dispatched": 0, "status": rollout.StatusCompleted})
		return
	}

	// Wave size is canary_percentage of the *original* eligible pool, not
	// however many remain — so start's canary batch and every later
	// advance's wave are the same size, and a rollout with 1,000 eligible
	// devices at 10% walks in ~10 waves rather than "canary, then
	// everyone else at once" (build plan §4 Phase 4 firm-up).
	total, err := h.rollouts.TotalDevices(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to count rollout total devices", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	waveSize := total * ro.CanaryPercentage / 100
	if waveSize < 1 {
		waveSize = 1
	}
	batch := eligible
	if waveSize < len(batch) {
		batch = eligible[:waveSize]
	}
	isFinalWave := len(batch) == len(eligible)

	operator := operatorFromRequest(r)
	dispatched := 0
	for _, deviceID := range batch {
		job, err := h.queueFirmwareDownload(r.Context(), deviceID, ro.FirmwareImageID, 0, operator)
		if err != nil {
			h.logger.Error("failed to queue rollout firmware download", "err", err, "rollout_id", id, "device_id", deviceID)
			continue
		}
		if err := h.rollouts.MarkDispatched(r.Context(), id, deviceID, job.ID); err != nil {
			h.logger.Error("failed to mark rollout device dispatched", "err", err, "rollout_id", id, "device_id", deviceID)
			continue
		}
		dispatched++
	}

	status := rollout.StatusActive
	if isCanaryStart {
		_ = h.rollouts.SetStatus(r.Context(), id, rollout.StatusActive)
	} else if isFinalWave {
		status = rollout.StatusCompleted
		_ = h.rollouts.SetStatus(r.Context(), id, rollout.StatusCompleted)
	}

	if err := h.auditor.Record(r.Context(), operator, "", "RolloutBatchDispatched", map[string]any{
		"rollout_id": id, "canary": isCanaryStart, "batch_size": len(batch), "dispatched": dispatched,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"dispatched": dispatched, "batch_size": len(batch), "status": status, "final_wave": isFinalWave,
	})
}

// dispatchRollback queues the rollback firmware image (if one was
// configured at rollout creation) to every device that actually received
// the bad build — devices whose download never succeeded have nothing to
// roll back. Idempotent: does nothing if this rollout already had a
// rollback dispatched (SetRollbackDispatched is only ever called once,
// from the one place BLOCKED is reached).
func (h *handler) dispatchRollback(ctx context.Context, ro *rollout.Rollout, operator string) {
	if ro.RollbackFirmwareImageID == nil || ro.RollbackDispatchedAt != nil {
		return
	}
	targets, err := h.rollouts.SuccessfulDeviceIDs(ctx, ro.ID)
	if err != nil {
		h.logger.Error("failed to list rollout rollback targets", "err", err, "rollout_id", ro.ID)
		return
	}
	dispatched := 0
	for _, deviceID := range targets {
		if _, err := h.queueFirmwareDownload(ctx, deviceID, *ro.RollbackFirmwareImageID, 0, operator); err != nil {
			h.logger.Error("failed to queue rollback firmware download", "err", err, "rollout_id", ro.ID, "device_id", deviceID)
			continue
		}
		dispatched++
	}
	if err := h.rollouts.SetRollbackDispatched(ctx, ro.ID); err != nil {
		h.logger.Error("failed to mark rollout rollback dispatched", "err", err, "rollout_id", ro.ID)
	}
	if err := h.auditor.Record(ctx, operator, "", "RolloutRollbackDispatched", map[string]any{
		"rollout_id": ro.ID, "rollback_firmware_image_id": *ro.RollbackFirmwareImageID, "targeted": len(targets), "dispatched": dispatched,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Warn("rollout blocked, rollback firmware dispatched", "rollout_id", ro.ID, "dispatched", dispatched, "targeted", len(targets))
}
