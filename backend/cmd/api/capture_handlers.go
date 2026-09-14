package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"acs/internal/captures"

	"github.com/google/uuid"
)

type startCaptureRequest struct {
	MatchType  string `json:"match_type"`
	MatchValue string `json:"match_value"`
	Protocol   string `json:"protocol"`
}

type captureSessionResponse struct {
	ID              string  `json:"id"`
	DeviceID        *string `json:"device_id,omitempty"`
	MatchType       string  `json:"match_type"`
	MatchValue      string  `json:"match_value"`
	Protocol        string  `json:"protocol"`
	Status          string  `json:"status"`
	EffectiveStatus string  `json:"effective_status"`
	StartedBy       string  `json:"started_by"`
	StartedAt       string  `json:"started_at"`
	StoppedAt       *string `json:"stopped_at,omitempty"`
	ExpiresAt       string  `json:"expires_at"`
}

func toCaptureSessionResponse(s captures.Session) captureSessionResponse {
	resp := captureSessionResponse{
		ID: s.ID, DeviceID: s.DeviceID, MatchType: s.MatchType, MatchValue: s.MatchValue,
		Protocol: s.Protocol, Status: s.Status, EffectiveStatus: s.EffectiveStatus(time.Now()),
		StartedBy: s.StartedBy, StartedAt: s.StartedAt.Format(time.RFC3339), ExpiresAt: s.ExpiresAt.Format(time.RFC3339),
	}
	if s.StoppedAt != nil {
		t := s.StoppedAt.Format(time.RFC3339)
		resp.StoppedAt = &t
	}
	return resp
}

func validMatchType(t string) bool {
	return t == captures.MatchDevice || t == captures.MatchIdentity || t == captures.MatchRemoteIP
}

func validProtocol(p string) bool {
	return p == "CWMP" || p == "USP"
}

func validCaptureID(w http.ResponseWriter, id string) bool {
	if _, err := uuid.Parse(id); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return false
	}
	return true
}

// createDeviceCapture implements POST /api/v1/devices/{id}/captures --
// the "by device" trigger mode (design §4), where match_value is
// resolved from the device's own oui_serial rather than supplied by the
// caller.
func (h *handler) createDeviceCapture(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	var req struct {
		Protocol string `json:"protocol"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !validProtocol(req.Protocol) {
		http.Error(w, `protocol must be "CWMP" or "USP"`, http.StatusBadRequest)
		return
	}

	deviceID := device.ID
	s, err := h.captures.Start(r.Context(), captures.StartParams{
		DeviceID: &deviceID, MatchType: captures.MatchDevice, MatchValue: device.OUISerial,
		Protocol: req.Protocol, StartedBy: operatorFromRequest(r), MaxDuration: h.captureMaxDuration,
	})
	if errors.Is(err, captures.ErrAlreadyActive) {
		http.Error(w, "an active capture already exists for this device", http.StatusConflict)
		return
	}
	if err != nil {
		h.logger.Error("failed to start device capture", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, toCaptureSessionResponse(*s))
}

// createCapture implements POST /api/v1/captures -- the "by expected
// identity" and "by remote IP" trigger modes (design §4), where the
// operator supplies match_value directly since no devices row is
// correlated yet.
func (h *handler) createCapture(w http.ResponseWriter, r *http.Request) {
	var req startCaptureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.MatchType != captures.MatchIdentity && req.MatchType != captures.MatchRemoteIP {
		http.Error(w, `match_type must be "identity" or "remote_ip" (use POST /api/v1/devices/{id}/captures for "device")`, http.StatusBadRequest)
		return
	}
	if req.MatchValue == "" {
		http.Error(w, "match_value is required", http.StatusBadRequest)
		return
	}
	if !validProtocol(req.Protocol) {
		http.Error(w, `protocol must be "CWMP" or "USP"`, http.StatusBadRequest)
		return
	}

	// An IP address carries no tenant identity, so a scoped operator cannot
	// safely target it. Identity captures are allowed for onboarding, but if
	// that identity is already known its device must be in the caller's scope
	// and is bound immediately to close the pre-correlation access window.
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var deviceID *string
	if scoped && req.MatchType == captures.MatchRemoteIP {
		http.Error(w, "remote_ip captures require global access", http.StatusForbidden)
		return
	}
	if req.MatchType == captures.MatchIdentity {
		d, lookupErr := h.devices.GetByOUIserial(r.Context(), req.MatchValue)
		switch {
		case lookupErr == nil:
			if scoped && !deviceInScope(d.CustomerID, customerIDs) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			deviceID = &d.ID
		case !errors.Is(lookupErr, sql.ErrNoRows):
			h.logger.Error("failed to resolve capture identity", "err", lookupErr, "match_value", req.MatchValue)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	s, err := h.captures.Start(r.Context(), captures.StartParams{
		DeviceID: deviceID, MatchType: req.MatchType, MatchValue: req.MatchValue, Protocol: req.Protocol,
		StartedBy: operatorFromRequest(r), MaxDuration: h.captureMaxDuration,
	})
	if errors.Is(err, captures.ErrAlreadyActive) {
		http.Error(w, "an active capture already exists for this target", http.StatusConflict)
		return
	}
	if err != nil {
		h.logger.Error("failed to start capture", "err", err, "match_type", req.MatchType, "match_value", req.MatchValue)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, toCaptureSessionResponse(*s))
}

// stopCapture implements POST /api/v1/captures/{id}/stop.
func (h *handler) stopCapture(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validCaptureID(w, id) {
		return
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s, err := h.captures.StopAccessible(r.Context(), id, operatorFromRequest(r), customerIDs, scoped)
	if errors.Is(err, captures.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to stop capture session", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toCaptureSessionResponse(*s))
}

// listCaptures implements GET /api/v1/captures.
func (h *handler) listCaptures(w http.ResponseWriter, r *http.Request) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var sessions []captures.Session
	if scoped {
		sessions, err = h.captures.ListAccessible(r.Context(), operatorFromRequest(r), customerIDs)
	} else {
		sessions, err = h.captures.List(r.Context())
	}
	if err != nil {
		h.logger.Error("failed to list capture sessions", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]captureSessionResponse, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, toCaptureSessionResponse(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type captureEventResponse struct {
	ID         string  `json:"id"`
	Seq        int     `json:"seq"`
	Direction  string  `json:"direction"`
	Kind       string  `json:"kind"`
	OccurredAt string  `json:"occurred_at"`
	Summary    string  `json:"summary"`
	Body       *string `json:"body,omitempty"`
}

// getCaptureEvents implements GET /api/v1/captures/{id}/events.
func (h *handler) getCaptureEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validCaptureID(w, id) {
		return
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_, events, err := h.captures.GetWithEventsAccessible(r.Context(), id, operatorFromRequest(r), customerIDs, scoped)
	if errors.Is(err, captures.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to list capture events", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]captureEventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, captureEventResponse{
			ID: e.ID, Seq: e.Seq, Direction: e.Direction, Kind: e.Kind,
			OccurredAt: e.OccurredAt.Format(time.RFC3339), Summary: e.Summary, Body: e.Body,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// exportCapture implements GET /api/v1/captures/{id}/export -- the full
// redacted transcript as a downloadable JSON file (design §7).
func (h *handler) exportCapture(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validCaptureID(w, id) {
		return
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s, events, err := h.captures.GetWithEventsAccessible(r.Context(), id, operatorFromRequest(r), customerIDs, scoped)
	if errors.Is(err, captures.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to list capture events for export", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	eventsOut := make([]captureEventResponse, 0, len(events))
	for _, e := range events {
		eventsOut = append(eventsOut, captureEventResponse{
			ID: e.ID, Seq: e.Seq, Direction: e.Direction, Kind: e.Kind,
			OccurredAt: e.OccurredAt.Format(time.RFC3339), Summary: e.Summary, Body: e.Body,
		})
	}
	w.Header().Set("Content-Disposition", `attachment; filename="capture-`+id+`.json"`)
	writeJSON(w, http.StatusOK, map[string]any{
		"session": toCaptureSessionResponse(*s),
		"events":  eventsOut,
	})
}
