// Connection request and diagnostics handlers (split out of main.go,
// audit P3.1).
package main

import (
	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/jobs"
	"encoding/json"
	"net/http"
)

type createConnectionRequestRequest struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

type reachabilityInfo struct {
	ConnectionRequestMode string `json:"connection_request_mode"`
	Recommendation        string `json:"recommendation"`
}

// createDiagnosticsPingRequest mirrors design doc v3 §10.1's IPPing input
// parameters. Only Host is required; the rest default to sane values a
// GenieACS-style ping test would use.
type createDiagnosticsPingRequest struct {
	Host                string `json:"host"`
	NumberOfRepetitions int    `json:"number_of_repetitions,omitempty"`
	Timeout             int    `json:"timeout,omitempty"`
	DataBlockSize       int    `json:"data_block_size,omitempty"`
	DSCP                int    `json:"dscp,omitempty"`
}

const (
	diagPingDefaultRepetitions = 4
	diagPingDefaultTimeoutMS   = 5000
	diagPingDefaultBlockSize   = 64
	// diagPingMaxAttempts caps how many times cmd/acs will requeue a
	// DIAGNOSTICS_PING job for another poll before giving up as a
	// TIMEOUT — a device whose DiagnosticsState never leaves "Requested"
	// would otherwise poll forever (build plan §4 Phase 5).
	diagPingMaxAttempts = 15
)

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

	vendor, paths := h.vendors.MatchCellularDiagnostics(device.Manufacturer)

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue cellular diagnostics refresh", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key":    job.CommandKey,
		"status":         job.Status,
		"matched_vendor": vendor,
		"parameters":     paths,
	})
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
// §10.1). Unlike the other job types, this one polls itself to
// completion — cmd/acs's dispatch loop requeues the same job for another
// GetParameterValues poll until DiagnosticsState leaves "Requested"
// instead of finalizing after one round-trip. The REST response here is
// just "queued"; poll GET /jobs/{command_key} for the outcome, same as
// CONNECTION_REQUEST and FIRMWARE_DOWNLOAD.
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
	if req.NumberOfRepetitions == 0 {
		req.NumberOfRepetitions = diagPingDefaultRepetitions
	}
	if req.Timeout == 0 {
		req.Timeout = diagPingDefaultTimeoutMS
	}
	if req.DataBlockSize == 0 {
		req.DataBlockSize = diagPingDefaultBlockSize
	}

	job, err := h.jobs.CreateWithMaxAttempts(r.Context(), id, jobs.TypeDiagnosticsPing,
		jobs.DiagnosticsPingPayload{
			Host:                req.Host,
			NumberOfRepetitions: req.NumberOfRepetitions,
			Timeout:             req.Timeout,
			DataBlockSize:       req.DataBlockSize,
			DSCP:                req.DSCP,
			Prefix:              adapters.DiagnosticsPrefix(device.DataModelRoot, adapters.DiagnosticPing),
		}, operatorFromRequest(r), diagPingMaxAttempts)
	if err != nil {
		h.logger.Error("failed to queue diagnostics ping", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
	})
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

const (
	diagTracerouteDefaultTries     = 3
	diagTracerouteDefaultTimeoutMS = 5000
	diagTracerouteDefaultBlockSize = 38
	diagTracerouteDefaultMaxHops   = 30
	// diagTracerouteMaxAttempts mirrors diagPingMaxAttempts — same poll-
	// loop shape, same reason for a cap (build plan §4 Phase 5).
	diagTracerouteMaxAttempts = 15
)

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
	if req.NumberOfTries == 0 {
		req.NumberOfTries = diagTracerouteDefaultTries
	}
	if req.Timeout == 0 {
		req.Timeout = diagTracerouteDefaultTimeoutMS
	}
	if req.DataBlockSize == 0 {
		req.DataBlockSize = diagTracerouteDefaultBlockSize
	}
	if req.MaxHopCount == 0 {
		req.MaxHopCount = diagTracerouteDefaultMaxHops
	}

	job, err := h.jobs.CreateWithMaxAttempts(r.Context(), id, jobs.TypeDiagnosticsTraceroute,
		jobs.DiagnosticsTraceroutePayload{
			Host:          req.Host,
			NumberOfTries: req.NumberOfTries,
			Timeout:       req.Timeout,
			DataBlockSize: req.DataBlockSize,
			DSCP:          req.DSCP,
			MaxHopCount:   req.MaxHopCount,
			Prefix:        adapters.DiagnosticsPrefix(device.DataModelRoot, adapters.DiagnosticTraceroute),
		}, operatorFromRequest(r), diagTracerouteMaxAttempts)
	if err != nil {
		h.logger.Error("failed to queue diagnostics traceroute", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
	})
}
