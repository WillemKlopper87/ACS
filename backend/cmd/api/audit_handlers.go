// Audit log REST surface (design doc v3 §11.8: "basic audit logging").
// The log itself has existed since Phase 1 (every write-shaped action
// already records to it); this is the first REST read path onto it.
package main

import (
	"net/http"
	"time"

	"acs/internal/observability"
)

type auditEntryResponse struct {
	ID         string `json:"id"`
	OccurredAt string `json:"occurred_at"`
	Actor      string `json:"actor"`
	DeviceID   string `json:"device_id,omitempty"`
	Action     string `json:"action"`
	Details    any    `json:"details,omitempty"`
}

func toAuditEntryResponse(e *observability.AuditEntry) auditEntryResponse {
	resp := auditEntryResponse{
		ID: e.ID, OccurredAt: e.OccurredAt.Format(time.RFC3339), Actor: e.Actor, Action: e.Action,
	}
	if e.DeviceID != nil {
		resp.DeviceID = *e.DeviceID
	}
	if len(e.Details) > 0 {
		resp.Details = e.Details
	}
	return resp
}

func (h *handler) listAuditLog(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device_id")
	action := r.URL.Query().Get("action")

	entries, err := h.auditor.List(r.Context(), deviceID, action)
	if err != nil {
		h.logger.Error("failed to list audit log", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	items := make([]auditEntryResponse, 0, len(entries))
	for _, e := range entries {
		items = append(items, toAuditEntryResponse(&e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
