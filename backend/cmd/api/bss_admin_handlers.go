// BSS integration admin panel (admin-platform backlog): a superadmin-only
// view onto the BSS-facing adapter (internal/bss, cmd/bssadapter) — (1)
// onboarding/setup, real CRUD against account_device_mappings and
// webhook_subscriptions via the same internal/bss repositories bssadapter
// itself uses (no proxying needed, both processes share one Postgres
// instance and this package can import internal/bss directly); (2)
// health/stats, real aggregate counts, no synthetic data; (3)
// troubleshooting scripts that make real HTTP calls through the live
// bssadapter process (not internal/bss directly) — the point is to
// exercise the actual request/response flow a BSS integrator would see,
// auth included, not just the underlying repository logic.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"acs/internal/bss"
)

// --- Section 1: onboarding/setup -------------------------------------------

type createBSSOAuthClientRequest struct {
	Name string `json:"name"`
}

// createBSSOAuthClient registers a new OAuth2 client-credentials
// integration — the client_secret is returned exactly once, here, same
// "shown once" rule as every other generated credential in this app.
func (h *handler) createBSSOAuthClient(w http.ResponseWriter, r *http.Request) {
	var req createBSSOAuthClientRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	client, secret, err := h.bssOAuthClients.CreateClient(r.Context(), req.Name)
	if err != nil {
		h.logger.Error("failed to create bss oauth client", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "BSSOAuthClientCreated", map[string]any{
		"name": req.Name, "client_id": client.ClientID,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client":        client,
		"client_secret": secret,
	})
}

func (h *handler) listBSSOAuthClients(w http.ResponseWriter, r *http.Request) {
	items, err := h.bssOAuthClients.ListClients(r.Context())
	if err != nil {
		h.logger.Error("failed to list bss oauth clients", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []bss.OAuthClient{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) revokeBSSOAuthClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.bssOAuthClients.RevokeClient(r.Context(), id); err != nil {
		http.Error(w, "oauth client not found or already revoked", http.StatusNotFound)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "BSSOAuthClientRevoked", map[string]any{"id": id}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) listBSSMappings(w http.ResponseWriter, r *http.Request) {
	items, err := h.bssMappings.ListAll(r.Context(), 500)
	if err != nil {
		h.logger.Error("failed to list bss mappings", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if items == nil {
		// ListAll returns a nil slice (not []T{}) when there are zero rows —
		// encodes as JSON null, which crashes the frontend's items.length
		// check. Coerce here rather than in the repository, since other
		// callers of ListAll may not share that assumption.
		items = []bss.AccountDeviceMapping{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createBSSMappingRequest struct {
	AccountID   string `json:"account_id"`
	OUISerial   string `json:"oui_serial"`
	ServicePlan string `json:"service_plan"`
}

func (h *handler) createBSSMapping(w http.ResponseWriter, r *http.Request) {
	var req createBSSMappingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.AccountID == "" || req.OUISerial == "" {
		http.Error(w, "account_id and oui_serial are required", http.StatusBadRequest)
		return
	}
	mapping, err := h.bssMappings.CreateMapping(r.Context(), req.AccountID, req.OUISerial, req.ServicePlan)
	if err == bss.ErrDeviceNotFound {
		http.Error(w, "no device found for that oui_serial — it must have sent at least one Inform first", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("failed to create bss mapping", "err", err, "account_id", req.AccountID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, mapping.DeviceID, "BSSMappingCreatedByAdmin", map[string]any{
		"account_id": req.AccountID, "oui_serial": req.OUISerial,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusOK, mapping)
}

func (h *handler) listBSSWebhooks(w http.ResponseWriter, r *http.Request) {
	items, err := h.bssWebhooks.ListSubscriptions(r.Context())
	if err != nil {
		h.logger.Error("failed to list webhook subscriptions", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []bss.WebhookSubscription{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createBSSWebhookRequest struct {
	AccountID  *string  `json:"account_id"`
	TargetURL  string   `json:"target_url"`
	Secret     string   `json:"secret"`
	EventTypes []string `json:"event_types"`
}

func (h *handler) createBSSWebhook(w http.ResponseWriter, r *http.Request) {
	var req createBSSWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.TargetURL == "" || req.Secret == "" || len(req.EventTypes) == 0 {
		http.Error(w, "target_url, secret, and at least one event type are required", http.StatusBadRequest)
		return
	}
	sub, err := h.bssWebhooks.CreateSubscription(r.Context(), req.AccountID, req.TargetURL, req.Secret, req.EventTypes)
	if err != nil {
		h.logger.Error("failed to create webhook subscription", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "BSSWebhookSubscriptionCreated", map[string]any{
		"target_url": req.TargetURL, "event_types": req.EventTypes,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusOK, sub)
}

func (h *handler) deleteBSSWebhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.bssWebhooks.DeleteSubscription(r.Context(), id); err == bss.ErrSubscriptionNotFound {
		http.Error(w, "webhook subscription not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to delete webhook subscription", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "BSSWebhookSubscriptionDeleted", map[string]any{"id": id}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Section 2: health/stats -------------------------------------------

func (h *handler) getBSSStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.bssMappings.Stats(r.Context())
	if err != nil {
		h.logger.Error("failed to compute bss stats", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

type bssHealthResponse struct {
	AdapterURL      string `json:"adapter_url"`
	Reachable       bool   `json:"reachable"`
	LatencyMS       int64  `json:"latency_ms"`
	TokenConfigured bool   `json:"token_configured"`
	Error           string `json:"error,omitempty"`
}

// getBSSHealth checks the live bssadapter process's /metrics endpoint —
// real reachability, not a database-only proxy for "is it up."
func (h *handler) getBSSHealth(w http.ResponseWriter, r *http.Request) {
	resp := bssHealthResponse{AdapterURL: h.bssAdapterURL, TokenConfigured: h.bssToken != ""}
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, h.bssAdapterURL+"/metrics", nil)
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	httpResp, err := h.bssHTTPClient.Do(req)
	resp.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	defer httpResp.Body.Close()
	resp.Reachable = httpResp.StatusCode == http.StatusOK
	if !resp.Reachable {
		resp.Error = fmt.Sprintf("adapter returned HTTP %d", httpResp.StatusCode)
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- Section 3: troubleshooting -------------------------------------------
//
// Every script below calls the real, live bssadapter over HTTP — the same
// process a BSS integrator's traffic hits — and returns exactly what came
// back (status code, latency, raw body), rather than reinterpreting it, so
// an operator sees the real request/response flow, faults included.

type adapterCallResult struct {
	Description string `json:"description"`
	Method      string `json:"method"`
	URL         string `json:"url"`
	StatusCode  int    `json:"status_code"`
	LatencyMS   int64  `json:"latency_ms"`
	Body        string `json:"body"`
	Error       string `json:"error,omitempty"`
}

// doAdapterCall performs one real HTTP call against the live bssadapter.
func (h *handler) doAdapterCall(r *http.Request, description, method, path string, body []byte, withAuth bool) adapterCallResult {
	url := h.bssAdapterURL + path
	res := adapterCallResult{Description: description, Method: method, URL: url}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, url, bodyReader)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if withAuth && h.bssToken != "" {
		req.Header.Set("Authorization", "Bearer "+h.bssToken)
	}

	start := time.Now()
	resp, err := h.bssHTTPClient.Do(req)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Body = string(b)
	return res
}

type troubleshootMappingRequest struct {
	AccountID string `json:"account_id"`
}

// troubleshootMappingLookup exercises Workflow A's read path (GET
// /bss/v1/mappings/{account_id}) exactly as a BSS caller would.
func (h *handler) troubleshootMappingLookup(w http.ResponseWriter, r *http.Request) {
	var req troubleshootMappingRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.AccountID == "" {
		http.Error(w, "account_id is required", http.StatusBadRequest)
		return
	}
	result := h.doAdapterCall(r, "Workflow A: list mappings for account", http.MethodGet, "/bss/v1/mappings/"+req.AccountID, nil, true)
	writeJSON(w, http.StatusOK, result)
}

// troubleshootAuthCheck demonstrates the adapter's auth enforcement with
// two real calls to the same endpoint: one with the configured bearer
// token, one without — an operator should see 401 on the second.
func (h *handler) troubleshootAuthCheck(w http.ResponseWriter, r *http.Request) {
	withToken := h.doAdapterCall(r, "with configured Authorization header", http.MethodGet, "/bss/v1/mappings/__auth_probe__", nil, true)
	withoutToken := h.doAdapterCall(r, "without Authorization header (expect 401)", http.MethodGet, "/bss/v1/mappings/__auth_probe__", nil, false)
	writeJSON(w, http.StatusOK, map[string]any{"with_token": withToken, "without_token": withoutToken})
}

type troubleshootJobStatusRequest struct {
	CommandKey string `json:"command_key"`
}

// troubleshootJobStatus exercises Workflow C exactly as a BSS caller would.
func (h *handler) troubleshootJobStatus(w http.ResponseWriter, r *http.Request) {
	var req troubleshootJobStatusRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.CommandKey == "" {
		http.Error(w, "command_key is required", http.StatusBadRequest)
		return
	}
	result := h.doAdapterCall(r, "Workflow C: job status lookup", http.MethodGet, "/bss/v1/jobs/"+req.CommandKey, nil, true)
	writeJSON(w, http.StatusOK, result)
}

type troubleshootOrderRequest struct {
	ExternalOrderID string `json:"external_order_id"`
	AccountID       string `json:"account_id"`
	WifiSSID        string `json:"wifi_ssid"`
	WifiPassword    string `json:"wifi_password"`
}

// troubleshootOrderDispatch exercises Workflow B end to end — this is a
// REAL order against the account's mapped device (MODIFY_WIFI, the only
// implemented action), not a dry run. It requires the caller to supply an
// external_order_id and account_id explicitly rather than picking one
// automatically, the same "no auto-fire against a real device" posture
// the CLI console's raw-CWMP mode already applies.
func (h *handler) troubleshootOrderDispatch(w http.ResponseWriter, r *http.Request) {
	var req troubleshootOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.ExternalOrderID == "" || req.AccountID == "" || (req.WifiSSID == "" && req.WifiPassword == "") {
		http.Error(w, "external_order_id, account_id, and at least one of wifi_ssid/wifi_password are required", http.StatusBadRequest)
		return
	}
	params := map[string]string{}
	if req.WifiSSID != "" {
		params["wifi_ssid"] = req.WifiSSID
	}
	if req.WifiPassword != "" {
		params["wifi_password"] = req.WifiPassword
	}
	body, _ := json.Marshal(map[string]any{
		"external_order_id": req.ExternalOrderID,
		"account_id":        req.AccountID,
		"service_type":      "INTERNET_SERVICE",
		"action":            "MODIFY_WIFI",
		"parameters":        params,
	})
	result := h.doAdapterCall(r, "Workflow B: real MODIFY_WIFI order dispatch (not a dry run)", http.MethodPost, "/bss/v1/orders", body, true)
	writeJSON(w, http.StatusOK, result)
}
