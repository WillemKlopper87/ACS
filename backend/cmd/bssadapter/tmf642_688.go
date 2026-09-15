package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"acs/internal/bss"
	"acs/internal/tmf/telemetry"
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
type tmf688HubRequest struct {
	Callback   string   `json:"callback"`
	Secret     string   `json:"secret"`
	AccountID  string   `json:"accountId"`
	EventTypes []string `json:"eventTypes"`
}

func tmfPage(total, offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return offset, end
}
func tmfPageQuery(r *http.Request, total int) (int, int) {
	offset, limit := 0, 50
	if n, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil {
		offset = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = n
	}
	return tmfPage(total, offset, limit)
}
func tmfSelectMap(m map[string]any, raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return m
	}
	out := map[string]any{}
	for _, key := range strings.Split(raw, ",") {
		key = strings.TrimSpace(key)
		if _, ok := m[key]; ok {
			out[key] = m[key]
		}
	}
	return out
}

func tmf642AlarmResponse(a *bss.AlarmRecord) map[string]any {
	return map[string]any{"id": a.ID, "href": "/tmf-api/alarmManagement/v4/alarm/" + a.ID, "alarmType": a.AlarmType, "perceivedSeverity": a.Severity, "state": a.State, "probableCause": a.ProbableCause, "specificProblem": a.SpecificProblem, "sourceKey": a.SourceKey, "accountId": a.AccountID, "deviceId": a.DeviceID, "serviceId": a.ServiceID, "details": json.RawMessage(a.Details), "raisedAt": a.RaisedAt, "clearedAt": a.ClearedAt}
}

func (h *handler) createTMF688Hub(w http.ResponseWriter, r *http.Request) {
	var req tmf688HubRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Callback) == "" || strings.TrimSpace(req.Secret) == "" || len(req.EventTypes) == 0 {
		writeError(w, 400, "ErrInvalidRequest", "callback, secret, and eventTypes are required")
		return
	}
	claims, ok := h.tmfPrincipal(w, r, bss.ScopeTMFWrite)
	if !ok || !tmfAccountAllowed(w, claims, strings.TrimSpace(req.AccountID)) {
		return
	}
	var account *string
	if strings.TrimSpace(req.AccountID) != "" {
		id := strings.TrimSpace(req.AccountID)
		account = &id
	}
	sub, err := h.webhooks.CreateSubscription(r.Context(), account, req.Callback, req.Secret, req.EventTypes)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	writeJSON(w, 201, map[string]any{"id": sub.ID, "callback": sub.TargetURL, "accountId": account, "eventTypes": sub.EventTypes, "createdAt": sub.CreatedAt})
}

func (h *handler) listTMF688Hubs(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.tmfPrincipal(w, r, bss.ScopeTMFRead)
	if !ok {
		return
	}
	subs, err := h.webhooks.ListSubscriptions(r.Context())
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	allowedAccounts := map[string]struct{}{}
	for _, id := range claims.AccountIDs {
		allowedAccounts[id] = struct{}{}
	}
	out := make([]map[string]any, 0, len(subs))
	for _, s := range subs {
		if !claims.GlobalAccess {
			if s.AccountID == nil {
				continue
			}
			if _, allowed := allowedAccounts[*s.AccountID]; !allowed {
				continue
			}
		}
		out = append(out, map[string]any{"id": s.ID, "callback": s.TargetURL, "accountId": s.AccountID, "eventTypes": s.EventTypes, "createdAt": s.CreatedAt})
	}
	offset, end := tmfPageQuery(r, len(out))
	page := out[offset:end]
	selected := make([]map[string]any, len(page))
	for i := range page {
		selected[i] = tmfSelectMap(page[i], r.URL.Query().Get("fields"))
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(out)))
	w.Header().Set("X-Result-Count", strconv.Itoa(len(page)))
	writeJSON(w, 200, selected)
}

func (h *handler) getTMF688Event(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID == "" {
		writeError(w, 400, "ErrInvalidRequest", "accountId is required")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFRead, accountID) {
		return
	}
	e, err := h.mappings.FindEventForAccount(r.Context(), strings.TrimSpace(r.PathValue("id")), accountID)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	if e == nil {
		writeError(w, 404, "ErrNotFound", "no such event")
		return
	}
	writeJSON(w, 200, map[string]any{"id": e.ID, "href": "/tmf-api/eventManagement/v4/event/" + e.ID, "eventType": e.EventType, "eventTime": e.EventTime, "sourceKey": e.SourceKey, "accountId": e.AccountID, "deviceId": e.DeviceID, "serviceId": e.ServiceID, "event": json.RawMessage(e.Payload)})
}

func (h *handler) getTMF642Alarm(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID == "" {
		writeError(w, 400, "ErrInvalidRequest", "accountId is required")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFRead, accountID) {
		return
	}
	a, err := h.mappings.FindAlarmForAccount(r.Context(), strings.TrimSpace(r.PathValue("id")), accountID)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	if a == nil {
		writeError(w, 404, "ErrNotFound", "no such alarm")
		return
	}
	writeJSON(w, 200, tmf642AlarmResponse(a))
}

func (h *handler) listTMF688Events(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	claims, ok := h.tmfPrincipal(w, r, bss.ScopeTMFRead)
	if !ok {
		return
	}
	if accountID == "" && !claims.GlobalAccess {
		writeError(w, 400, "ErrInvalidRequest", "accountId is required for scoped clients")
		return
	}
	if accountID != "" && !tmfAccountAllowed(w, claims, accountID) {
		return
	}
	events, err := h.mappings.ListEvents(r.Context(), accountID, 100)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		out = append(out, map[string]any{"id": e.ID, "eventType": e.EventType, "eventTime": e.EventTime, "sourceKey": e.SourceKey, "accountId": e.AccountID, "deviceId": e.DeviceID, "serviceId": e.ServiceID, "event": json.RawMessage(e.Payload)})
	}
	offset, end := tmfPageQuery(r, len(out))
	page := out[offset:end]
	selected := make([]map[string]any, len(page))
	for i := range page {
		selected[i] = tmfSelectMap(page[i], r.URL.Query().Get("fields"))
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(out)))
	w.Header().Set("X-Result-Count", strconv.Itoa(len(page)))
	writeJSON(w, 200, selected)
}

func (h *handler) listTMF642Alarms(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	claims, ok := h.tmfPrincipal(w, r, bss.ScopeTMFRead)
	if !ok {
		return
	}
	if accountID == "" && !claims.GlobalAccess {
		writeError(w, 400, "ErrInvalidRequest", "accountId is required for scoped clients")
		return
	}
	if accountID != "" && !tmfAccountAllowed(w, claims, accountID) {
		return
	}
	alarms, err := h.mappings.ListAlarms(r.Context(), accountID, strings.TrimSpace(r.URL.Query().Get("state")), 100)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(alarms))
	for _, a := range alarms {
		out = append(out, map[string]any{"id": a.ID, "alarmType": a.AlarmType, "perceivedSeverity": a.Severity, "state": a.State, "sourceKey": a.SourceKey, "accountId": a.AccountID, "deviceId": a.DeviceID, "serviceId": a.ServiceID, "raisedAt": a.RaisedAt, "clearedAt": a.ClearedAt})
	}
	offset, end := tmfPageQuery(r, len(out))
	page := out[offset:end]
	selected := make([]map[string]any, len(page))
	for i := range page {
		selected[i] = tmfSelectMap(page[i], r.URL.Query().Get("fields"))
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(out)))
	w.Header().Set("X-Result-Count", strconv.Itoa(len(page)))
	writeJSON(w, 200, selected)
}

func (h *handler) patchTMF642Alarm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State string `json:"state"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || (body.State != "acknowledged" && body.State != "cleared") {
		writeError(w, 400, "ErrInvalidRequest", "state must be acknowledged or cleared")
		return
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID == "" {
		writeError(w, 400, "ErrInvalidRequest", "accountId is required")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFAcknowledge, accountID) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := h.mappings.UpdateAlarmStateForAccount(r.Context(), id, accountID, body.State); err != nil {
		writeError(w, 404, "ErrNotFound", "no such alarm")
		return
	}
	a, err := h.mappings.FindAlarmForAccount(r.Context(), id, accountID)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	if a == nil {
		writeError(w, 404, "ErrNotFound", "no such alarm")
		return
	}
	writeJSON(w, 200, tmf642AlarmResponse(a))
}

func (h *handler) createTMF688Event(w http.ResponseWriter, r *http.Request) {
	var req tmf688EventRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.SourceKey) == "" || strings.TrimSpace(req.EventType) == "" {
		writeError(w, 400, "ErrInvalidRequest", "sourceKey and eventType are required")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFWrite, strings.TrimSpace(req.AccountID)) {
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
	// Recovery events are retained above, then may close only the matching
	// tenant/device alarm. Requiring faultCode prevents a generic "online"
	// notification from clearing unrelated conditions.
	if telemetry.QualifyingRecoveryEvent(req.EventType) {
		if code, ok := req.Payload["faultCode"].(string); ok && strings.TrimSpace(code) != "" {
			protocol := "ACS"
			if p, ok := req.Payload["protocol"].(string); ok && strings.TrimSpace(p) != "" {
				protocol = p
			}
			if err := h.mappings.ClearAlarm(r.Context(), req.AccountID, req.DeviceID, telemetry.ConditionKey(protocol, req.DeviceID, code)); err != nil && !errors.Is(err, sql.ErrNoRows) {
				h.logger.Warn("failed to clear recovered TMF alarm", "err", err, "device_id", req.DeviceID)
			}
		}
	}
	writeJSON(w, 201, e)
}

func (h *handler) createTMF642Alarm(w http.ResponseWriter, r *http.Request) {
	var req tmf642AlarmRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.SourceKey) == "" || strings.TrimSpace(req.AlarmType) == "" || strings.TrimSpace(req.Severity) == "" {
		writeError(w, 400, "ErrInvalidRequest", "sourceKey, alarmType, and perceivedSeverity are required")
		return
	}
	if !h.authorizeTMF(w, r, bss.ScopeTMFWrite, strings.TrimSpace(req.AccountID)) {
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
