package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type tmf688EventRequest struct {
	SourceKey string         `json:"sourceKey"`
	AccountID string         `json:"accountId"`
	DeviceID  string         `json:"deviceId"`
	ServiceID string         `json:"serviceId"`
	EventType string         `json:"eventType"`
	EventTime *time.Time     `json:"eventTime"`
	Payload   map[string]any `json:"payload"`
}
type tmf642AlarmRequest struct {
	SourceKey       string         `json:"sourceKey"`
	AccountID       string         `json:"accountId"`
	DeviceID        string         `json:"deviceId"`
	ServiceID       string         `json:"serviceId"`
	AlarmType       string         `json:"alarmType"`
	Severity        string         `json:"perceivedSeverity"`
	ProbableCause   string         `json:"probableCause"`
	SpecificProblem string         `json:"specificProblem"`
	Details         map[string]any `json:"details"`
}

func (h *handler) createTMF688Event(w http.ResponseWriter, r *http.Request) {
	var req tmf688EventRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.SourceKey) == "" || strings.TrimSpace(req.EventType) == "" {
		writeError(w, 400, "ErrInvalidRequest", "sourceKey and eventType are required")
		return
	}
	at := time.Now().UTC()
	if req.EventTime != nil {
		at = req.EventTime.UTC()
	}
	payload, _ := json.Marshal(req.Payload)
	e, err := h.mappings.CreateEvent(r.Context(), req.SourceKey, req.AccountID, req.DeviceID, req.ServiceID, req.EventType, payload, at)
	if err != nil {
		h.logger.Error("failed to record TMF688 event", "err", err)
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	if e == nil {
		writeJSON(w, 200, map[string]any{"status": "duplicate", "sourceKey": req.SourceKey})
		return
	}
	writeJSON(w, 201, e)
}

func (h *handler) createTMF642Alarm(w http.ResponseWriter, r *http.Request) {
	var req tmf642AlarmRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.SourceKey) == "" || strings.TrimSpace(req.AlarmType) == "" || strings.TrimSpace(req.Severity) == "" {
		writeError(w, 400, "ErrInvalidRequest", "sourceKey, alarmType, and perceivedSeverity are required")
		return
	}
	allowed := map[string]bool{"critical": true, "major": true, "minor": true, "warning": true, "indeterminate": true}
	if !allowed[strings.ToLower(req.Severity)] {
		writeError(w, 400, "ErrInvalidRequest", "invalid perceivedSeverity")
		return
	}
	details, _ := json.Marshal(req.Details)
	a, err := h.mappings.CreateAlarm(r.Context(), req.SourceKey, req.AccountID, req.DeviceID, req.ServiceID, req.AlarmType, strings.ToLower(req.Severity), req.ProbableCause, req.SpecificProblem, details)
	if err != nil {
		h.logger.Error("failed to record TMF642 alarm", "err", err)
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	if a == nil {
		writeJSON(w, 200, map[string]any{"status": "duplicate", "sourceKey": req.SourceKey})
		return
	}
	writeJSON(w, 201, a)
}
