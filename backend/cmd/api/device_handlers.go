// Device read/list/parameter/bulk handlers and the tenancy scope guard
// (split out of main.go, audit P3.1).
package main

import (
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/operators"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// deviceResponse is the v3 §8.1/§8.2 device shape, trimmed to the fields
// Phase 1/3 actually populate.
type deviceResponse struct {
	ID                          string   `json:"id"`
	OUISerial                   string   `json:"oui_serial"`
	Manufacturer                string   `json:"manufacturer"`
	OUI                         string   `json:"oui"`
	ProductClass                string   `json:"product_class"`
	SerialNumber                string   `json:"serial_number"`
	DataModelRoot               string   `json:"data_model_root"`
	OnlineStatus                string   `json:"online_status"`
	LastInformAt                *string  `json:"last_inform_at,omitempty"`
	LastInformEventCodes        []string `json:"last_inform_event_codes,omitempty"`
	ConnectionRequestURL        *string  `json:"connection_request_url,omitempty"`
	ConnectionRequestMode       string   `json:"connection_request_mode"`
	LastConnectionRequestAt     *string  `json:"last_connection_request_at,omitempty"`
	LastConnectionRequestStatus *string  `json:"last_connection_request_status,omitempty"`
	Tags                        []string `json:"tags,omitempty"`
	CWMPAuthMode                string   `json:"cwmp_auth_mode"`
	UDPConnectionRequestAddress *string  `json:"udp_connection_request_address,omitempty"`
	NATDetected                 *bool    `json:"nat_detected,omitempty"`
	CustomerID                  *string  `json:"customer_id,omitempty"`
	Location                    *string  `json:"location,omitempty"`
}

func toResponse(d devices.Device) deviceResponse {
	resp := deviceResponse{
		ID:                          d.ID,
		OUISerial:                   d.OUISerial,
		Manufacturer:                d.Manufacturer,
		OUI:                         d.OUI,
		ProductClass:                d.ProductClass,
		SerialNumber:                d.SerialNumber,
		DataModelRoot:               d.DataModelRoot,
		OnlineStatus:                d.OnlineStatus,
		LastInformEventCodes:        d.LastInformEventCodes,
		ConnectionRequestURL:        d.ConnectionRequestURL,
		ConnectionRequestMode:       d.ConnectionRequestMode,
		LastConnectionRequestStatus: d.LastConnectionRequestStatus,
		Tags:                        d.Tags,
		CWMPAuthMode:                d.CWMPAuthMode,
		UDPConnectionRequestAddress: d.UDPConnectionRequestAddress,
		NATDetected:                 d.NATDetected,
		CustomerID:                  d.CustomerID,
		Location:                    d.Location,
	}
	if d.LastInformAt != nil {
		s := d.LastInformAt.Format(time.RFC3339)
		resp.LastInformAt = &s
	}
	if d.LastConnectionRequestAt != nil {
		s := d.LastConnectionRequestAt.Format(time.RFC3339)
		resp.LastConnectionRequestAt = &s
	}
	return resp
}

// deviceScope resolves the calling operator's multi-tenancy scope
// (admin-platform backlog) into devices.ListParams' CustomerIDs/Scoped
// fields — unrestricted (Scoped: false) when auth is disabled, the caller
// is superadmin, or the operator holds the explicit GlobalAccess grant
// (audit P0.1). Every other operator is scoped to exactly their assigned
// customers/regions; zero scope rows resolves to zero accessible
// customers, not unrestricted access — GlobalAccess must be granted
// deliberately by a superadmin, it is never inferred from an empty scope
// result.
//
// Lookup failures return an error and the caller must fail the request
// (audit P0.2): the previous behavior of treating a failed scope
// resolution as "unrestricted" turned transient DB errors into a
// cross-tenant data exposure.
func (h *handler) deviceScope(r *http.Request) (customerIDs []string, scoped bool, err error) {
	if len(h.jwtSecret) == 0 {
		return nil, false, nil
	}
	claims, ok := operatorClaims(r.Context())
	if !ok || claims.Role == operators.RoleSuperAdmin {
		return nil, false, nil
	}
	op, err := h.operators.ByUsername(r.Context(), claims.Subject)
	if err != nil {
		h.logger.Error("failed to resolve operator for scoping", "err", err, "username", claims.Subject)
		return nil, false, fmt.Errorf("resolve operator for scoping: %w", err)
	}
	if op.GlobalAccess {
		return nil, false, nil
	}
	ids, isScoped, err := h.tenancy.AccessibleCustomerIDs(r.Context(), op.ID)
	if err != nil {
		h.logger.Error("failed to resolve operator scope", "err", err, "operator_id", op.ID)
		return nil, false, fmt.Errorf("resolve operator scope: %w", err)
	}
	return ids, isScoped, nil
}

// getScopedDevice loads a device and enforces the calling operator's
// tenancy scope in one place (audit P0.2) — every device-addressed
// handler goes through here instead of calling h.devices.Get directly.
// It writes the HTTP response itself on failure: 404 both when the
// device doesn't exist and when it is outside the caller's scope (a 403
// would confirm the device exists across a tenant boundary), 500 when
// the device or scope lookup errors — a failed scope resolution denies
// access, it no longer falls open.
func (h *handler) getScopedDevice(w http.ResponseWriter, r *http.Request, id string) (*devices.Device, bool) {
	d, err := h.devices.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		h.logger.Error("failed to get device", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if scoped && !deviceInScope(d.CustomerID, customerIDs) {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	return d, true
}

func (h *handler) listDevices(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	result, err := h.devices.List(r.Context(), devices.ListParams{Page: page, PageSize: pageSize, CustomerIDs: customerIDs, Scoped: scoped})
	if err != nil {
		h.logger.Error("failed to list devices", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	items := make([]deviceResponse, 0, len(result.Items))
	for _, d := range result.Items {
		items = append(items, toResponse(d))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": result.Total,
	})
}

// listDevicesSummary backs a mass-review view: fleet counts grouped by
// vendor/status/reachability, computed in SQL so it stays cheap
// regardless of fleet size — the alternative (paging through every
// device to count client-side) is exactly the thing pagination above
// exists to avoid.
func (h *handler) listDevicesSummary(w http.ResponseWriter, r *http.Request) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	groups, err := h.devices.Summary(r.Context(), customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to summarize devices", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if groups == nil {
		// Summary returns a nil slice on a fleet with zero devices —
		// encodes as JSON null, which crashes Fleet Control's groups.map().
		groups = []devices.GroupCount{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

// listMatchingDeviceIDs backs Fleet Control's "select all N matching this
// filter" — build plan §6.2's stated scope boundary (selection accumulated
// across pages, but nothing let an operator select everything matching a
// filter without paging through it by hand). Filters mirror what Fleet
// Control already computes client-side from the grouped summary strip and
// its search box.
func (h *handler) listMatchingDeviceIDs(w http.ResponseWriter, r *http.Request) {
	filter := devices.MatchingFilter{
		Manufacturer:          r.URL.Query().Get("manufacturer"),
		OnlineStatus:          r.URL.Query().Get("online_status"),
		ConnectionRequestMode: r.URL.Query().Get("connection_request_mode"),
		Search:                r.URL.Query().Get("search"),
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ids, err := h.devices.MatchingIDs(r.Context(), filter, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to list matching device ids", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ids": ids, "count": len(ids)})
}

// fleetHealth aggregates the signals design doc v3 §16.1 names for a
// health screen (inform rate, RPC fault rate, connection request success
// rate, device online/offline/unreachable counts) into one response —
// build plan's stated gap "Fleet Health screen — not built". Every number
// here is a live SQL aggregate, not a cached/estimated figure.
func (h *handler) fleetHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byStatus, err := h.devices.CountByOnlineStatus(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count devices by online status", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byReachability, err := h.devices.CountByReachability(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count devices by reachability", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	informRecency, err := h.devices.InformRecencyBuckets(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to bucket inform recency", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Job stats aren't scoped to the operator's devices — jobs don't carry
	// a customer_id of their own, and joining through devices for this one
	// read isn't worth it yet (build plan note, not a security gap: job
	// stats reveal fleet-wide operational health, not any specific
	// customer's device identities or data).
	jobStats, err := h.jobs.StatusCountsSince(ctx, time.Now().UTC().Add(-24*time.Hour), customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count job statuses", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	jobTotal := 0
	for _, n := range jobStats {
		jobTotal += n
	}
	successRate := 0.0
	if jobTotal > 0 {
		successRate = float64(jobStats["SUCCESS"]) / float64(jobTotal) * 100
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"devices_by_status":       byStatus,
		"devices_by_reachability": byReachability,
		"inform_recency":          informRecency,
		"jobs_last_24h":           jobStats,
		"jobs_last_24h_total":     jobTotal,
		"job_success_rate_pct":    successRate,
		"generated_at":            time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *handler) getDevice(w http.ResponseWriter, r *http.Request) {
	d, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toResponse(*d))
}

func deviceInScope(deviceCustomerID *string, accessibleCustomerIDs []string) bool {
	if deviceCustomerID == nil {
		return false // unassigned devices are invisible to a scoped operator, not implicitly shared
	}
	for _, id := range accessibleCustomerIDs {
		if id == *deviceCustomerID {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// cachedValueResponse mirrors design doc v3 §7.7's example cache entry
// shape.
type cachedValueResponse struct {
	Value     string `json:"value"`
	Type      string `json:"type,omitempty"`
	UpdatedAt string `json:"updated_at"`
	Source    string `json:"source"`
}

// getParameters reads the device's parameter cache (design doc v3 §8.3:
// "Read cached parameters" — this is a cache read, not a live CPE query;
// PUT below is what queues a job that actually talks to the device).
// An optional ?paths=a,b,c filters to just those parameter names.
func (h *handler) getParameters(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	cached, err := h.params.Get(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to read parameter cache", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var wanted map[string]bool
	if raw := r.URL.Query().Get("paths"); raw != "" {
		wanted = map[string]bool{}
		for _, p := range strings.Split(raw, ",") {
			wanted[strings.TrimSpace(p)] = true
		}
	}

	out := make(map[string]cachedValueResponse, len(cached))
	for name, v := range cached {
		if wanted != nil && !wanted[name] {
			continue
		}
		out[name] = cachedValueResponse{Value: v.Value, Type: v.Type, UpdatedAt: v.UpdatedAt.Format(time.RFC3339), Source: v.Source}
	}

	writeJSON(w, http.StatusOK, map[string]any{"parameters": out})
}

// getParameterHistory backs the nice-to-have feature backlog's parameter
// value history: how a specific parameter's cached value has changed over
// time, not just its current reading.
func (h *handler) getParameterHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name query parameter is required", http.StatusBadRequest)
		return
	}
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	entries, err := h.params.History(r.Context(), id, name)
	if err != nil {
		h.logger.Error("failed to read parameter history", "err", err, "id", id, "name", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	items := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		items = append(items, map[string]any{
			"value": e.Value, "type": e.Type, "source": e.Source, "recorded_at": e.RecordedAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "items": items})
}

// parameterInput is the wire shape for a parameter write, shared by the
// single-device and bulk endpoints so the two don't drift.
type parameterInput struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

func buildSetParameterPayload(inputs []parameterInput) (jobs.SetParameterPayload, error) {
	if len(inputs) == 0 {
		return jobs.SetParameterPayload{}, errors.New("parameters must not be empty")
	}
	payload := jobs.SetParameterPayload{Parameters: make([]jobs.ParameterWrite, len(inputs))}
	for i, p := range inputs {
		if p.Name == "" {
			return jobs.SetParameterPayload{}, errors.New("each parameter requires a name")
		}
		payload.Parameters[i] = jobs.ParameterWrite{Name: p.Name, Value: p.Value, Type: p.Type}
	}
	return payload, nil
}

type putParametersRequest struct {
	Parameters []parameterInput `json:"parameters"`
}

// putParameters queues a SET_PARAMETER job and returns 202 Accepted
// (design doc v3 §8.3/§19.2 — REST write endpoints never talk to the CPE
// directly, they queue a job that the CWMP gateway dispatches on the
// device's next session).
func (h *handler) putParameters(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	var req putParametersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	payload, err := buildSetParameterPayload(req.Parameters)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeSetParameter, payload, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue parameter write", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
	})
}

// maxBulkDevices caps one bulk-action request the same way maxPageSize
// caps one list response — a mass-control view is exactly the place an
// accidental "select all" against an 18,000-device fleet would otherwise
// try to fire 18,000 jobs from a single HTTP request.
const maxBulkDevices = 500

type bulkActionRequest struct {
	DeviceIDs      []string         `json:"device_ids,omitempty"`
	GroupID        string           `json:"group_id,omitempty"` // alternative to device_ids — targets a device_groups member set (build plan §4 Phase 7)
	Action         string           `json:"action"`             // SET_PARAMETER | CONNECTION_REQUEST | REFRESH_CELLULAR
	Parameters     []parameterInput `json:"parameters,omitempty"`
	TimeoutSeconds int              `json:"timeout_seconds,omitempty"`
}

type bulkActionResult struct {
	DeviceID   string `json:"device_id"`
	CommandKey string `json:"command_key,omitempty"`
	Error      string `json:"error,omitempty"`
}

// bulkAction is the "mass unit control" capability a fleet-scale review
// view needs and no single-device endpoint provides: one action fanned
// out to N devices, each getting its own independent job/command_key.
// A failure on one device (e.g. it no longer exists) doesn't block the
// others — the response reports per-device outcome so the caller can see
// exactly which ones didn't queue, rather than one all-or-nothing result
// hiding which devices actually got the action.
func (h *handler) bulkAction(w http.ResponseWriter, r *http.Request) {
	var req bulkActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if len(req.DeviceIDs) == 0 && req.GroupID != "" {
		memberIDs, err := h.groups.MemberDeviceIDs(r.Context(), req.GroupID)
		if err != nil {
			h.logger.Error("failed to resolve device group", "err", err, "group_id", req.GroupID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		req.DeviceIDs = memberIDs
	}
	if len(req.DeviceIDs) == 0 {
		http.Error(w, "device_ids (or a non-empty group_id) must be provided", http.StatusBadRequest)
		return
	}
	if len(req.DeviceIDs) > maxBulkDevices {
		http.Error(w, fmt.Sprintf("device_ids exceeds the %d-device limit per request", maxBulkDevices), http.StatusBadRequest)
		return
	}

	var setParamPayload jobs.SetParameterPayload
	if req.Action == jobs.TypeSetParameter {
		payload, err := buildSetParameterPayload(req.Parameters)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		setParamPayload = payload
	} else if req.Action != jobs.TypeConnectionRequest && req.Action != "REFRESH_CELLULAR" {
		http.Error(w, fmt.Sprintf("unsupported bulk action %q", req.Action), http.StatusBadRequest)
		return
	}

	// Per-device tenancy enforcement (audit P0.2): a scoped operator's
	// bulk request must not fan out to devices outside their scope —
	// out-of-scope IDs report the same "not found" a single-device
	// endpoint would return, without blocking the in-scope remainder.
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	operator := operatorFromRequest(r)
	results := make([]bulkActionResult, 0, len(req.DeviceIDs))

	for _, deviceID := range req.DeviceIDs {
		result := bulkActionResult{DeviceID: deviceID}

		if scoped {
			d, err := h.devices.Get(r.Context(), deviceID)
			if err != nil || !deviceInScope(d.CustomerID, customerIDs) {
				result.Error = "not found"
				results = append(results, result)
				continue
			}
		}

		switch req.Action {
		case jobs.TypeSetParameter:
			job, err := h.jobs.Create(r.Context(), deviceID, jobs.TypeSetParameter, setParamPayload, operator)
			if err != nil {
				result.Error = err.Error()
			} else {
				result.CommandKey = job.CommandKey
			}

		case jobs.TypeConnectionRequest:
			job, err := h.jobs.Create(r.Context(), deviceID, jobs.TypeConnectionRequest,
				jobs.ConnectionRequestPayload{TimeoutSeconds: req.TimeoutSeconds}, operator)
			if err != nil {
				result.Error = err.Error()
			} else {
				result.CommandKey = job.CommandKey
			}

		case "REFRESH_CELLULAR":
			device, err := h.devices.Get(r.Context(), deviceID)
			if err != nil {
				result.Error = "device not found"
				break
			}
			_, paths := h.vendors.MatchCellularDiagnostics(device.Manufacturer)
			job, err := h.jobs.Create(r.Context(), deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, operator)
			if err != nil {
				result.Error = err.Error()
			} else {
				result.CommandKey = job.CommandKey
			}
		}

		results = append(results, result)
	}

	succeeded := 0
	for _, res := range results {
		if res.Error == "" {
			succeeded++
		}
	}

	if err := h.auditor.Record(r.Context(), operator, "", "BulkAction", map[string]any{
		"action": req.Action, "device_count": len(req.DeviceIDs), "succeeded": succeeded,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("bulk action dispatched", "action", req.Action, "devices", len(req.DeviceIDs), "succeeded", succeeded)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"action":    req.Action,
		"requested": len(req.DeviceIDs),
		"succeeded": succeeded,
		"results":   results,
	})
}
