// Connection request and diagnostics handlers (split out of main.go,
// audit P3.1).
package main

import (
	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/diagnostics"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

type createConnectionRequestRequest struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

type reachabilityInfo struct {
	ConnectionRequestMode string `json:"connection_request_mode"`
	Recommendation        string `json:"recommendation"`
}

// createDiagnosticsPingRequest mirrors design doc v3 §10.1's IPPing input
// parameters. Only Host is required; the rest default to
// internal/diagnostics' Ping defaults (shared with scheduled pings).
type createDiagnosticsPingRequest struct {
	Host                string `json:"host"`
	NumberOfRepetitions int    `json:"number_of_repetitions,omitempty"`
	Timeout             int    `json:"timeout,omitempty"`
	DataBlockSize       int    `json:"data_block_size,omitempty"`
	DSCP                int    `json:"dscp,omitempty"`
}

// discoveredNames returns the device's discovered parameter names (nil if
// discovery never ran), writing a 500 and returning false on a read error.
func (h *handler) discoveredNames(w http.ResponseWriter, r *http.Request, deviceID string) (map[string]bool, bool) {
	discovered, err := h.params.GetNames(r.Context(), deviceID)
	if err != nil {
		h.logger.Error("failed to read discovered parameter names", "err", err, "device_id", deviceID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if discovered == nil {
		return nil, true
	}
	return discovered.Names, true
}

// writeDiagnosticQueued answers a queued diagnostic, listing the optional
// inputs discovery did not prove writable (the CPE's own defaults apply).
func writeDiagnosticQueued(w http.ResponseWriter, job *jobs.Job, skipped []string) {
	body := map[string]any{"command_key": job.CommandKey, "status": job.Status}
	if len(skipped) > 0 {
		body["skipped_inputs"] = skipped
	}
	writeJSON(w, http.StatusAccepted, body)
}

// createConnectionRequest queues a CONNECTION_REQUEST job (design doc v3
// §8.4). Unlike PUT parameters, this never talks to the CPE synchronously
// from the request handler — the background worker in connreq_worker.go
// picks it up. reachability in the response reflects the device's
// *current* known classification from previous attempts, not this
// attempt's outcome, which is still pending when this responds.
func (h *handler) createConnectionRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	var req createConnectionRequestRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeConnectionRequest,
		jobs.ConnectionRequestPayload{TimeoutSeconds: req.TimeoutSeconds}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue connection request", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	recommendation := "Device may rely on periodic Inform until reachability is verified."
	if device.ConnectionRequestMode == devices.ModeDirectIPv4 || device.ConnectionRequestMode == devices.ModeDirectIPv6 {
		recommendation = "Device has previously confirmed direct reachability."
	} else if device.ConnectionRequestMode == devices.ModePeriodicFallback {
		recommendation = "Device has previously failed to respond to Connection Request within the wait window (likely CGNAT) — periodic Inform is the reliable path."
	}
	if device.ConnectionRequestURL == nil || *device.ConnectionRequestURL == "" {
		recommendation = "No ConnectionRequestURL known for this device yet — it must send at least one Inform first."
	}
	if device.ConnectionRequestMode == devices.ModeXMPP {
		recommendation = "Device has previously been reached by XMPP Connection Request (TR-069 Annex K); it is tried first."
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
		"reachability": reachabilityInfo{
			ConnectionRequestMode: device.ConnectionRequestMode,
			Recommendation:        recommendation,
		},
	})
}

// refreshCellularDiagnostics queues a GET_PARAMETER job for the device's
// vendor-specific RF/signal diagnostic parameters (RSRP, RSRQ, SINR,
// CellID, ...), matched by the device's reported manufacturer against the
// vendor catalogs in internal/devices/adapters. This is the same
// branch-by-manufacturer pattern a GenieACS provision script uses to poll
// 5G signal metrics, built on the GET_PARAMETER job type Phase 2 already
// has — falls back to the generic TR-181 Cellular path set for an
// unrecognized vendor rather than failing.
func (h *handler) refreshCellularDiagnostics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	vendor, paths, qualified := h.cellularDiagnosticPaths(r.Context(), device)
	if len(paths) == 0 {
		http.Error(w, "no discovered cellular metrics; run parameter discovery", http.StatusConflict)
		return
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue cellular diagnostics refresh", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key":       job.CommandKey,
		"status":            job.Status,
		"matched_vendor":    vendor,
		"profile_qualified": qualified,
		"parameters":        paths,
	})
}

// getDiagnosticsCapabilities reports only diagnostics objects the device
// advertised during discovery. It does not run a throughput test or accept a
// target URL; those operations require a later, separately rate-limited job.
func (h *handler) getDiagnosticsCapabilities(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	discovered, err := h.params.GetNames(r.Context(), device.ID)
	if err != nil {
		h.logger.Error("failed to read diagnostics capabilities", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var names map[string]bool
	if discovered != nil {
		names = discovered.Names
	}
	ping, traceroute := diagnostics.PingTracerouteCapabilities(names,
		adapters.DiagnosticsPrefix(device.DataModelRoot, adapters.DiagnosticPing),
		adapters.DiagnosticsPrefix(device.DataModelRoot, adapters.DiagnosticTraceroute))
	writeJSON(w, http.StatusOK, map[string]any{
		"discovered_at": discoveredAtString(discovered),
		"tr143":         adapters.ResolveTR143Capability(names),
		"ping":          ping,
		"traceroute":    traceroute,
	})
}

func discoveredAtString(discovered *parameters.DiscoveredNames) any {
	if discovered == nil {
		return nil
	}
	return discovered.DiscoveredAt.Format(time.RFC3339)
}

// cellularDiagnosticPaths prefers the device's discovered model over a
// vendor baseline. This lets every Zyxel 5G firmware expose its own
// X_ZYXEL_* radio metrics without the ACS having to guess or ship one XML
// profile per firmware image. Static profiles remain the safe pre-discovery
// fallback.
func (h *handler) cellularDiagnosticPaths(ctx context.Context, device *devices.Device) (string, []string, bool) {
	vendor, fallback, qualified := h.vendors.MatchCellularDiagnosticsForDevice(device.Manufacturer, device.ProductClass)
	if h.params == nil {
		return vendor, fallback, qualified
	}
	discovered, err := h.params.GetNames(ctx, device.ID)
	if err != nil || discovered == nil {
		return vendor, fallback, qualified
	}
	if paths := adapters.CellularDiagnosticParamsFromNames(discovered.Names); len(paths) > 0 {
		return vendor, paths, qualified
	}
	return vendor, nil, false
}

// refreshWifiClients queues a GET_PARAMETER job over the whole WiFi
// AccessPoint subtree — every SSID's AssociatedDevice table included —
// mirroring refreshCellularDiagnostics' shape.
func (h *handler) refreshWifiClients(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	paths := []string{adapters.WiFiAssociatedDevicesPrefix(device.DataModelRoot)}
	job, err := h.jobs.Create(r.Context(), id, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue wifi clients refresh", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
		"parameters":  paths,
	})
}

// createDiagnosticsPing queues a DIAGNOSTICS_PING job (design doc v3
// §10.1). The job triggers the test, waits for the CPE's next session
// (ideally the "8 DIAGNOSTICS COMPLETE" Inform) and polls the result there
// — see internal/jobs/diagnostics.go. The device must have discovered a
// writable IPPing object (409 otherwise: e.g. a Nokia Beacon 3.1 has no
// Device.IP.Diagnostics. at all), and only inputs discovery proved
// writable are sent. Poll GET /jobs/{command_key} for the outcome.
func (h *handler) createDiagnosticsPing(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	var req createDiagnosticsPingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Host == "" {
		http.Error(w, "host is required", http.StatusBadRequest)
		return
	}
	payload := jobs.DiagnosticsPingPayload{
		Host:                req.Host,
		NumberOfRepetitions: req.NumberOfRepetitions,
		Timeout:             req.Timeout,
		DataBlockSize:       req.DataBlockSize,
		DSCP:                req.DSCP,
		Prefix:              adapters.DiagnosticsPrefix(device.DataModelRoot, adapters.DiagnosticPing),
	}
	diagnostics.ApplyPingDefaults(&payload)
	names, ok := h.discoveredNames(w, r, id)
	if !ok {
		return
	}
	plan, err := diagnostics.PlanPing(names, &payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	job, err := h.jobs.CreateWithMaxAttempts(r.Context(), id, jobs.TypeDiagnosticsPing, payload,
		operatorFromRequest(r), jobs.DiagnosticsMaxAttempts)
	if err != nil {
		h.logger.Error("failed to queue diagnostics ping", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeDiagnosticQueued(w, job, plan.Skipped)
}

// createDiagnosticsTracerouteRequest mirrors design doc v3 §10.1's
// TraceRoute input parameters — build plan §4 Phase 5's explicitly
// deferred item ("identical pattern [to Ping], not committed to this
// session"), built here.
type createDiagnosticsTracerouteRequest struct {
	Host          string `json:"host"`
	NumberOfTries int    `json:"number_of_tries,omitempty"`
	Timeout       int    `json:"timeout,omitempty"`
	DataBlockSize int    `json:"data_block_size,omitempty"`
	DSCP          int    `json:"dscp,omitempty"`
	MaxHopCount   int    `json:"max_hop_count,omitempty"`
}

// createDiagnosticsTraceroute is createDiagnosticsPing for TraceRoute, with
// the same discovery gating (the FastMile's TraceRoute has no Timeout or
// DSCP leaves, so those inputs are skipped rather than faulting 9005).
func (h *handler) createDiagnosticsTraceroute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}

	var req createDiagnosticsTracerouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Host == "" {
		http.Error(w, "host is required", http.StatusBadRequest)
		return
	}
	payload := jobs.DiagnosticsTraceroutePayload{
		Host:          req.Host,
		NumberOfTries: req.NumberOfTries,
		Timeout:       req.Timeout,
		DataBlockSize: req.DataBlockSize,
		DSCP:          req.DSCP,
		MaxHopCount:   req.MaxHopCount,
		Prefix:        adapters.DiagnosticsPrefix(device.DataModelRoot, adapters.DiagnosticTraceroute),
	}
	diagnostics.ApplyTracerouteDefaults(&payload)
	names, ok := h.discoveredNames(w, r, id)
	if !ok {
		return
	}
	plan, err := diagnostics.PlanTraceroute(names, &payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	job, err := h.jobs.CreateWithMaxAttempts(r.Context(), id, jobs.TypeDiagnosticsTraceroute, payload,
		operatorFromRequest(r), jobs.DiagnosticsMaxAttempts)
	if err != nil {
		h.logger.Error("failed to queue diagnostics traceroute", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeDiagnosticQueued(w, job, plan.Skipped)
}

type tr143Request struct {
	URL            string `json:"url"`
	TestFileLength uint64 `json:"test_file_length,omitempty"`
}

func (h *handler) createTR143(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, ok := h.getScopedDevice(w, r, id)
	if !ok {
		return
	}
	var req tr143Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}
	allowed := strings.Split(os.Getenv("ACS_TR143_ALLOWED_HOSTS"), ",")
	if err := diagnostics.ValidateTR143Target(req.URL, allowed); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	discovered, err := h.params.GetNames(r.Context(), id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var names map[string]bool
	if discovered != nil {
		names = discovered.Names
	}
	capability := adapters.ResolveTR143Capability(names)
	direction := r.PathValue("direction")
	if direction != "upload" && direction != "download" {
		http.Error(w, `direction must be "download" or "upload"`, http.StatusBadRequest)
		return
	}
	if req.TestFileLength > 100*1024*1024 {
		http.Error(w, "test_file_length exceeds 100 MiB limit", http.StatusBadRequest)
		return
	}
	if req.TestFileLength == 0 {
		req.TestFileLength = 1048576
	}

	upload := direction == "upload"
	path, command := capability.DownloadPath, capability.DownloadCommand
	if upload {
		path, command = capability.UploadPath, capability.UploadCommand
	}
	if path == "" && command == "" {
		http.Error(w, "device has not advertised this TR-143 capability", http.StatusConflict)
		return
	}
	// A CWMP device (object path, no USP command) gets a wire plan from
	// discovery: URL and DiagnosticsState must be writable, and the poll
	// reads only this direction's discovered result leaves.
	var plan diagnostics.Plan
	if command == "" {
		plan, err = diagnostics.PlanTR143(names, path, req.URL, upload, req.TestFileLength)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}

	var jobType string
	var payload any
	if upload {
		jobType, payload = jobs.TypeDiagnosticsUpload, jobs.DiagnosticsUploadPayload{URL: req.URL, Prefix: path, USPCommand: command, TestFileLength: req.TestFileLength, DiagnosticsPlan: plan.DiagnosticsPlan}
	} else {
		jobType, payload = jobs.TypeDiagnosticsDownload, jobs.DiagnosticsDownloadPayload{URL: req.URL, Prefix: path, USPCommand: command, DiagnosticsPlan: plan.DiagnosticsPlan}
	}
	job, err := h.jobs.CreateWithMaxAttempts(r.Context(), id, jobType, payload, operatorFromRequest(r), jobs.DiagnosticsMaxAttempts)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeDiagnosticQueued(w, job, plan.Skipped)
}
