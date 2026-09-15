package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"acs/internal/bss"
	"github.com/google/uuid"
)

type tmf656CreateRequest struct {
	ExternalID     string `json:"externalId"`
	AccountID      string `json:"accountId"`
	ServiceID      string `json:"serviceId"`
	ProblemType    string `json:"problemType"`
	Description    string `json:"description"`
	Priority       string `json:"priority"`
	RelatedAlarmID string `json:"relatedAlarmId"`
}

type tmf656StatusRequest struct {
	Status     string `json:"status"`
	Resolution string `json:"resolution"`
}

func tmf656ProblemResponse(p *bss.ServiceProblemRecord) map[string]any {
	response := map[string]any{"id": p.ID, "href": "/tmf-api/serviceProblemManagement/v4/serviceProblem/" + p.ID, "externalId": p.ExternalID, "status": p.Status, "priority": p.Priority, "problemType": p.ProblemType, "description": p.Description}
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
	if req.ExternalID != "" {
		if existing, err := h.mappings.FindServiceProblem(r.Context(), req.ExternalID); err != nil {
			writeError(w, 500, "ErrInternal", "internal error")
			return
		} else if existing != nil {
			writeJSON(w, http.StatusOK, tmf656ProblemResponse(existing))
			return
		}
	}
	p, err := h.mappings.CreateServiceProblem(r.Context(), req.ExternalID, req.AccountID, req.ServiceID, req.ProblemType, req.Description, req.Priority, req.RelatedAlarmID)
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
	p, err := h.mappings.FindServiceProblem(r.Context(), strings.TrimSpace(r.PathValue("id")))
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
	writeJSON(w, 200, responses)
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
	if err := h.mappings.UpdateServiceProblemStatus(r.Context(), id, req.Status, req.Resolution); errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "ErrNotFound", "no such service problem")
		return
	} else if err != nil {
		writeError(w, 500, "ErrInternal", "internal error")
		return
	}
	h.getTMF656Problem(w, r)
}
