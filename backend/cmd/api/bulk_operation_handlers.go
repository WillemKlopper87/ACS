// Operator job control (cancel, retry) and bulk operations: the record of
// a fan-out, its progress, cancelling what is still queued and retrying
// what failed (docs/DEVICE-MANAGEMENT-PLAN.md).
package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"acs/internal/bulkops"
	"acs/internal/jobs"
	"acs/internal/operators"
	"acs/internal/parameters"
)

// allowed wraps hasPermission for routes where the permission needed
// depends on what the request touches. It writes the refusal itself and
// reports whether to continue.
func (h *handler) allowed(w http.ResponseWriter, r *http.Request, perm string) bool {
	granted, err := h.hasPermission(r, perm)
	if err != nil {
		h.logger.Error("failed to check permission", "err", err, "permission", perm)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if !granted {
		http.Error(w, "insufficient permission", http.StatusForbidden)
		return false
	}
	return true
}

// jobPermission is the permission the endpoint that queues a job of this
// type asks for. Cancelling or retrying one asks for the same.
func jobPermission(jobType string) string {
	switch jobType {
	case jobs.TypeConnectionRequest:
		return operators.PermConnectionReq
	case jobs.TypeDiagnosticsPing, jobs.TypeDiagnosticsTraceroute, jobs.TypeDiagnosticsDownload,
		jobs.TypeDiagnosticsUpload, jobs.TypeParameterDiscovery:
		return operators.PermDiagnosticsRun
	case jobs.TypeFirmwareDownload:
		return operators.PermFirmwareManage
	case jobs.TypeUpload:
		return operators.PermUploadRequest
	default:
		return operators.PermDevicesWrite
	}
}

// writeJobActionError maps a cancel/retry refusal to a status. Every
// "not in a state for that" answer is a 409 with the reason as its body.
func (h *handler) writeJobActionError(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, bulkops.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, jobs.ErrNotCancellable), errors.Is(err, jobs.ErrWorkflowOwned),
		errors.Is(err, jobs.ErrNotRetryable), errors.Is(err, jobs.ErrSecretsErased):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, parameters.ErrProtectedParameter):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		h.logger.Error("failed to "+what, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// scopedJob loads a job by command key and checks the caller may see its
// device and holds the permission its type needs.
func (h *handler) scopedJob(w http.ResponseWriter, r *http.Request) (*jobs.Job, bool) {
	job, err := h.jobs.ByCommandKey(r.Context(), r.PathValue("command_key"))
	if err != nil {
		h.writeJobActionError(w, err, "load job")
		return nil, false
	}
	if _, ok := h.getScopedDevice(w, r, job.DeviceID); !ok {
		return nil, false
	}
	if !h.allowed(w, r, jobPermission(job.Type)) {
		return nil, false
	}
	return job, true
}

// cancelJob withdraws a job that is still QUEUED.
func (h *handler) cancelJob(w http.ResponseWriter, r *http.Request) {
	job, ok := h.scopedJob(w, r)
	if !ok {
		return
	}
	operator := operatorFromRequest(r)
	cancelled, err := h.jobs.Cancel(r.Context(), job.ID, operator)
	if err != nil {
		h.writeJobActionError(w, err, "cancel job")
		return
	}
	if err := h.auditor.Record(r.Context(), operator, job.DeviceID, "JobCancelled", map[string]any{
		"command_key": job.CommandKey, "type": job.Type,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusOK, toJobResponse(cancelled))
}

// retryJob queues a new job with the payload of one that ended FAILED,
// TIMEOUT or CANCELLED, and returns the new job.
func (h *handler) retryJob(w http.ResponseWriter, r *http.Request) {
	job, ok := h.scopedJob(w, r)
	if !ok {
		return
	}
	operator := operatorFromRequest(r)
	retry, err := h.jobs.Retry(r.Context(), job.ID, operator)
	if err != nil {
		h.writeJobActionError(w, err, "retry job")
		return
	}
	if err := h.auditor.Record(r.Context(), operator, job.DeviceID, "JobRetried", map[string]any{
		"command_key": retry.CommandKey, "retry_of": job.CommandKey, "type": job.Type,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusAccepted, toJobResponse(retry))
}

// recordBulkOperation stores a fan-out's results as a bulk operation and
// returns its ID, or "" when it could not be stored: the jobs are already
// queued by then, so the caller still reports them.
func (h *handler) recordBulkOperation(ctx context.Context, kind, action, label, groupID, operator string, results []bulkActionResult) string {
	items := make([]bulkops.NewItem, len(results))
	for i, res := range results {
		items[i] = bulkops.NewItem{DeviceID: res.DeviceID, CommandKey: res.CommandKey, Error: res.Error, Detail: res.Detail}
	}
	id, err := h.bulkOps.Create(ctx, kind, action, label, groupID, operator, items)
	if err != nil {
		h.logger.Error("failed to record bulk operation", "err", err, "kind", kind, "action", action)
		return ""
	}
	return id
}

// bulkActionLabel says what a bulk action asked for. Names only: a label
// is stored and listed, so it never carries a value.
func bulkActionLabel(req bulkActionRequest) string {
	switch req.Action {
	case jobs.TypeSetParameter:
		names := make([]string, 0, len(req.Parameters))
		for _, p := range req.Parameters {
			names = append(names, p.Name)
		}
		return "Set " + strings.Join(names, ", ")
	case bulkActionSetWiFi:
		var what []string
		if req.WiFiSSID != "" {
			what = append(what, "network name")
		}
		if req.WiFiPassword != "" {
			what = append(what, "password")
		}
		if req.WiFiEnabled != nil {
			what = append(what, "on/off")
		}
		return "Set Wi-Fi " + strings.Join(what, ", ")
	case jobs.TypeConnectionRequest:
		return "Connection request"
	case "REFRESH_CELLULAR":
		return "Refresh cellular readings"
	default:
		return req.Action
	}
}

type bulkOperationResponse struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Action    string  `json:"action"`
	Label     string  `json:"label"`
	GroupID   *string `json:"group_id,omitempty"`
	CreatedBy string  `json:"created_by"`
	CreatedAt string  `json:"created_at"`
	State     string  `json:"state"`
	bulkops.Counts
}

type bulkOperationItemResponse struct {
	DeviceID    string  `json:"device_id"`
	OUISerial   *string `json:"oui_serial,omitempty"`
	DeviceLabel *string `json:"device_label,omitempty"`
	CommandKey  *string `json:"command_key,omitempty"`
	JobType     *string `json:"job_type,omitempty"`
	JobStatus   *string `json:"job_status,omitempty"`
	FaultString *string `json:"fault_string,omitempty"`
	Error       *string `json:"error,omitempty"`
	Detail      *string `json:"detail,omitempty"`
	Retries     int     `json:"retries"`
}

func toBulkOperationResponse(o *bulkops.Operation) bulkOperationResponse {
	return bulkOperationResponse{
		ID: o.ID, Kind: o.Kind, Action: o.Action, Label: o.Label, GroupID: o.GroupID,
		CreatedBy: o.CreatedBy, CreatedAt: o.CreatedAt.Format(time.RFC3339),
		State: o.State(), Counts: o.Counts,
	}
}

func (h *handler) bulkScope(w http.ResponseWriter, r *http.Request) (bulkops.Scope, bool) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return bulkops.Scope{}, false
	}
	return bulkops.Scope{CustomerIDs: customerIDs, Scoped: scoped}, true
}

func (h *handler) listBulkOperations(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.bulkScope(w, r)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	ops, total, err := h.bulkOps.List(r.Context(), scope, page, pageSize)
	if err != nil {
		h.logger.Error("failed to list bulk operations", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]bulkOperationResponse, 0, len(ops))
	for i := range ops {
		items = append(items, toBulkOperationResponse(&ops[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// writeBulkOperation answers with the operation as it stands now.
func (h *handler) writeBulkOperation(w http.ResponseWriter, r *http.Request, scope bulkops.Scope, status int, extra map[string]any) {
	op, items, err := h.bulkOps.Get(r.Context(), r.PathValue("id"), scope)
	if err != nil {
		h.writeJobActionError(w, err, "get bulk operation")
		return
	}
	out := make([]bulkOperationItemResponse, 0, len(items))
	for _, it := range items {
		out = append(out, bulkOperationItemResponse{
			DeviceID: it.DeviceID, OUISerial: it.OUISerial, DeviceLabel: it.DeviceLabel,
			CommandKey: it.CommandKey, JobType: it.JobType, JobStatus: it.JobStatus,
			FaultString: it.FaultString, Error: it.Error, Detail: it.Detail, Retries: it.Retries,
		})
	}
	body := map[string]any{"operation": toBulkOperationResponse(op), "items": out}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, status, body)
}

func (h *handler) getBulkOperation(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.bulkScope(w, r)
	if !ok {
		return
	}
	h.writeBulkOperation(w, r, scope, http.StatusOK, nil)
}

// bulkOperationForChange loads the operation in scope and checks the
// permission that starting one of its kind asks for.
func (h *handler) bulkOperationForChange(w http.ResponseWriter, r *http.Request) (*bulkops.Operation, bulkops.Scope, bool) {
	scope, ok := h.bulkScope(w, r)
	if !ok {
		return nil, scope, false
	}
	op, _, err := h.bulkOps.Get(r.Context(), r.PathValue("id"), scope)
	if err != nil {
		h.writeJobActionError(w, err, "get bulk operation")
		return nil, scope, false
	}
	perm := operators.PermBulkActions
	if op.Kind == bulkops.KindTemplateApply {
		perm = operators.PermTemplateManage
	}
	if !h.allowed(w, r, perm) {
		return nil, scope, false
	}
	return op, scope, true
}

// cancelBulkOperation cancels the operation's jobs that are still QUEUED.
// Jobs already sent to a device are left to finish.
func (h *handler) cancelBulkOperation(w http.ResponseWriter, r *http.Request) {
	op, scope, ok := h.bulkOperationForChange(w, r)
	if !ok {
		return
	}
	operator := operatorFromRequest(r)
	cancelled, err := h.bulkOps.CancelQueued(r.Context(), op.ID, scope, operator)
	if err != nil {
		h.writeJobActionError(w, err, "cancel bulk operation")
		return
	}
	if err := h.auditor.Record(r.Context(), operator, "", "BulkOperationCancelled", map[string]any{
		"bulk_operation_id": op.ID, "label": op.Label, "cancelled": cancelled,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.writeBulkOperation(w, r, scope, http.StatusOK, map[string]any{"cancelled": cancelled})
}

// retryFailedBulkOperation queues a new job for each device whose job
// ended FAILED or TIMEOUT.
func (h *handler) retryFailedBulkOperation(w http.ResponseWriter, r *http.Request) {
	op, scope, ok := h.bulkOperationForChange(w, r)
	if !ok {
		return
	}
	operator := operatorFromRequest(r)
	outcome, err := h.bulkOps.RetryFailed(r.Context(), op.ID, scope, operator)
	if err != nil {
		h.writeJobActionError(w, err, "retry bulk operation")
		return
	}
	if err := h.auditor.Record(r.Context(), operator, "", "BulkOperationRetried", map[string]any{
		"bulk_operation_id": op.ID, "label": op.Label, "retried": outcome.Retried, "skipped": outcome.Skipped,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.writeBulkOperation(w, r, scope, http.StatusAccepted, map[string]any{"retried": outcome.Retried, "skipped": outcome.Skipped})
}
