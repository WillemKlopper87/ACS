package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"acs/internal/bss"
	"github.com/google/uuid"
)

type tmf656CreateRequest struct {
	ExternalID          string   `json:"externalId"`
	AccountID           string   `json:"accountId"`
	ServiceID           string   `json:"serviceId"`
	ProblemType         string   `json:"problemType"`
	Description         string   `json:"description"`
	Priority            string   `json:"priority"`
	RelatedAlarmID      string   `json:"relatedAlarmId"`
	RelatedEventIDs     []string `json:"relatedEventIds"`
	AffectedResourceIDs []string `json:"affectedResourceIds"`
	Impact              string   `json:"impact"`
	Severity            string   `json:"severity"`
	RootCause           string   `json:"rootCause"`
}

type tmf656StatusRequest struct {
	Status     string `json:"status"`
	Resolution string `json:"resolution"`
}

func normalizeTMF656IDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return out
}

func tmf656ProblemResponse(p *bss.ServiceProblemRecord) map[string]any {
	response := map[string]any{"id": p.ID, "href": "/tmf-api/serviceProblemManagement/v4/serviceProblem/" + p.ID, "externalId": p.ExternalID, "status": p.Status, "priority": p.Priority, "problemType": p.ProblemType, "description": p.Description, "accountId": p.AccountID, "serviceId": p.ServiceID, "impact": p.Impact, "severity": p.Severity, "rootCause": p.RootCause, "relatedEventIds": p.RelatedEventIDs, "affectedResourceIds": p.AffectedResourceIDs, "createdAt": p.CreatedAt}
	if p.RelatedAlarmID != "" {
		response["relatedAlarm"] = map[string]any{"id": p.RelatedAlarmID}
	}
	return response
}

func (h *handler) createTMF656Problem(w http.ResponseWriter, r *http.Request) {
	var req tmf656CreateRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.AccountID) == "" || strings.TrimSpace(req.ProblemType) == "" || strings.TrimSpace(req.Description) == "" {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "accountId, problemType, and description are required")
		return
	}
	req.RelatedEventIDs = normalizeTMF656IDs(req.RelatedEventIDs)
	req.AffectedResourceIDs = normalizeTMF656IDs(req.AffectedResourceIDs)
	if req.Severity != "" && !map[string]bool{"critical": true, "major": true, "minor": true, "warning": true, "indeterminate": true}[strings.ToLower(req.Severity)] {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "severity is invalid")
		return
	}
	if req.ExternalID != "" {
		if existing, err := h.mappings.FindServiceProblemForAccount(r.Context(), req.ExternalID, req.AccountID); err != nil {
			writeError(w, 500, "ErrInternal", "internal error")
			return
		} else if existing != nil {
			writeJSON(w, http.StatusOK, tmf656ProblemResponse(existing))
			return
		}
	}
	if req.RelatedAlarmID != "" {
		if alarm, err := h.mappings.FindAlarmForAccount(r.Context(), req.RelatedAlarmID, req.AccountID); err != nil || alarm == nil {
			writeError(w, http.StatusBadRequest, "ErrInvalidRelation", "related alarm is not in the requested account")
			return
		}
	}
	for _, eventID := range req.RelatedEventIDs {
		if event, err := h.mappings.FindEventForAccount(r.Context(), eventID, req.AccountID); err != nil || event == nil {
			writeError(w, http.StatusBadRequest, "ErrInvalidRelation", "related event is not in the requested account")
			return
		}
	}
	p, err := h.mappings.CreateServiceProblemRich(r.Context(), req.ExternalID, req.AccountID, req.ServiceID, req.ProblemType, req.Description, req.Priority, req.RelatedAlarmID, req.RelatedEventIDs, req.AffectedResourceIDs, req.Impact, req.Severity, req.RootCause)
	if err != nil {
		h.logger.Error("failed to create TMF656 service problem", "err", err)
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	if p == nil {
		writeError(w, 409, "ErrConflict", "service problem already exists")
		return
	}
	writeJSON(w, http.StatusCreated, tmf656ProblemResponse(p))
}

func (h *handler) getTMF656Problem(w http.ResponseWriter, r *http.Request) {
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID == "" {
		writeError(w, 400, "ErrInvalidRequest", "accountId is required")
		return
	}
	p, err := h.mappings.FindServiceProblemForAccount(r.Context(), strings.TrimSpace(r.PathValue("id")), accountID)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	if p == nil {
		writeError(w, 404, "ErrNotFound", "no such service problem")
		return
	}
	writeJSON(w, 200, tmf656ProblemResponse(p))
}

func (h *handler) listTMF656Problems(w http.ResponseWriter, r *http.Request) {
	problems, err := h.mappings.ListServiceProblems(r.Context(), strings.TrimSpace(r.URL.Query().Get("accountId")), strings.TrimSpace(r.URL.Query().Get("status")), 500)
	if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	responses := make([]map[string]any, 0, len(problems))
	for i := range problems {
		p := problems[i]
		responses = append(responses, tmf656ProblemResponse(&p))
	}
	offset, end := tmfPageQuery(r, len(responses))
	page := responses[offset:end]
	selected := make([]map[string]any, len(page))
	for i := range page {
		selected[i] = tmfSelectMap(page[i], r.URL.Query().Get("fields"))
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(responses)))
	w.Header().Set("X-Result-Count", strconv.Itoa(len(page)))
	writeJSON(w, 200, selected)
}

func (h *handler) patchTMF656Problem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, 400, "ErrInvalidRequest", "invalid service problem id")
		return
	}
	var req tmf656StatusRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || !map[string]bool{"inProgress": true, "resolved": true, "closed": true}[req.Status] {
		writeError(w, 400, "ErrInvalidRequest", "status must be inProgress, resolved, or closed")
		return
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID == "" {
		writeError(w, 400, "ErrInvalidRequest", "accountId is required for lifecycle updates")
		return
	}
	if err := h.mappings.UpdateServiceProblemStatus(r.Context(), id, accountID, req.Status, req.Resolution); errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "ErrNotFound", "no such service problem")
		return
	} else if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	h.getTMF656Problem(w, r)
}
