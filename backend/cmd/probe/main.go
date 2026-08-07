// Command probe is the Phase 0 lab harness (build plan §4 Phase 0 / design
// doc v3 §14 Phase 0): a throwaway CWMP listener used only to characterize
// real devices before the production gateway is built. It accepts a real
// CPE's Inform, probes it with GetRPCMethods and GetParameterNames against
// both data model roots, and records the results as the raw material for
// the device compatibility matrix. It intentionally has no database, no
// job queue, and no REST API, and does not run in production — that is
// cmd/acs (Phase 1+, build plan §4 Phase 1).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"acs/internal/auth"
	"acs/internal/cwmp"
)

// maxBodyBytes guards against the "oversized XML" chaos scenario (design
// doc v3 §15.5) — a misbehaving or malicious CPE posting an unbounded body.
const maxBodyBytes = 4 << 20 // 4 MiB

const sessionIdleTimeout = 5 * time.Minute

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: levelFromEnv(),
	}))

	addr := envOr("ACS_ADDR", ":7547")
	resultsPath := envOr("ACS_RESULTS_FILE", "device-probe-results.jsonl")

	authr := auth.DigestAuthenticator{
		Username: os.Getenv("ACS_DIGEST_USERNAME"),
		Password: os.Getenv("ACS_DIGEST_PASSWORD"),
	}
	if !authr.Enabled() {
		logger.Warn("ACS_DIGEST_USERNAME/ACS_DIGEST_PASSWORD not set — CWMP endpoint is running WITHOUT authentication. Lab use only; never do this in production (design doc v3 §11.1/§11.2).")
	}

	resultsFile, err := os.OpenFile(resultsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		logger.Error("failed to open results file", "path", resultsPath, "err", err)
		os.Exit(1)
	}
	defer resultsFile.Close()

	store := cwmp.NewSessionStore()
	stopSweep := make(chan struct{})
	go sweepLoop(store, stopSweep)
	defer close(stopSweep)

	h := &handler{
		logger:  logger,
		auth:    authr,
		store:   store,
		results: resultsFile,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/cwmp", h.handleCWMP)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	certFile := os.Getenv("ACS_TLS_CERT")
	keyFile := os.Getenv("ACS_TLS_KEY")

	errCh := make(chan error, 1)
	go func() {
		if certFile != "" && keyFile != "" {
			logger.Info("CWMP gateway listening (TLS)", "addr", addr, "path", "/cwmp")
			errCh <- server.ListenAndServeTLS(certFile, keyFile)
		} else {
			logger.Warn("CWMP gateway listening WITHOUT TLS — lab use only (design doc v3 §11.1 requires HTTPS-only in production)", "addr", addr, "path", "/cwmp")
			errCh <- server.ListenAndServe()
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
}

func sweepLoop(store *cwmp.SessionStore, stop <-chan struct{}) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			store.Sweep(sessionIdleTimeout)
		case <-stop:
			return
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func levelFromEnv() slog.Level {
	if os.Getenv("ACS_DEBUG") != "" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// handler wires the CWMP protocol logic (internal/cwmp) to the HTTP
// transport, auth, and result recording.
type handler struct {
	logger  *slog.Logger
	auth    auth.DigestAuthenticator
	store   *cwmp.SessionStore
	results *os.File
}

// handleCWMP is the single endpoint a CPE talks to. It authenticates,
// parses the SOAP envelope, and dispatches:
//   - Inform            -> start/resume a probe session, respond InformResponse
//   - empty body         -> "any work for me?" poll, dispatch next probe RPC
//   - RPC response/Fault -> record the result, dispatch next probe RPC
func (h *handler) handleCWMP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "CWMP requires POST", http.StatusMethodNotAllowed)
		return
	}

	if h.auth.Enabled() && !h.auth.Verify(r) {
		h.logger.Warn("authentication failed or missing", "remote", r.RemoteAddr)
		h.auth.Challenge(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.Warn("failed to read request body (possibly oversized)", "err", err, "remote", r.RemoteAddr)
		http.Error(w, "body too large or unreadable", http.StatusBadRequest)
		return
	}

	h.logger.Debug("raw SOAP received", "body", string(raw), "remote", r.RemoteAddr)

	env, err := cwmp.ParseEnvelope(raw)
	if err != nil {
		h.logger.Error("malformed SOAP/XML", "err", err, "remote", r.RemoteAddr, "body", string(raw))
		http.Error(w, "malformed XML", http.StatusBadRequest)
		return
	}

	if env.Body.Inform != nil {
		// Echo the Inform's cwmp:ID and namespace version back on the
		// response (TR-069 §3.4.1.1) — strict CPEs abort on a mismatch.
		respID := env.Header.ID
		if respID == "" {
			respID = cwmp.NewID()
		}
		h.handleInform(w, env.Body.Inform, respID, cwmp.DetectCWMPNamespace(raw))
		return
	}

	session, ok := h.sessionFromCookie(r)
	if !ok {
		h.logger.Warn("request outside a known session (missing/unrecognized session cookie); nothing to do", "remote", r.RemoteAddr)
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusOK)
		return
	}

	if !env.Body.IsEmpty() {
		if env.Body.Fault != nil {
			h.logger.Warn("CPE returned fault", "device", session.DeviceKey,
				"code", env.Body.Fault.CWMPCode(), "message", env.Body.Fault.CWMPMessage())
		}
		session.CompleteCurrent(env.Body)
	}

	h.dispatchNext(w, session)
}

func (h *handler) handleInform(w http.ResponseWriter, inform *cwmp.Inform, respID, ns string) {
	events := inform.EventCodes()
	h.logger.Info("Inform received",
		"manufacturer", inform.DeviceId.Manufacturer,
		"oui", inform.DeviceId.OUI,
		"product_class", inform.DeviceId.ProductClass,
		"serial", inform.DeviceId.SerialNumber,
		"events", events,
	)

	session := h.store.StartOrResume(inform.DeviceId, events)

	http.SetCookie(w, &http.Cookie{
		Name:     "acs_session",
		Value:    session.DeviceKey,
		Path:     "/",
		HttpOnly: true,
	})
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cwmp.RenderInformResponseNS(respID, ns))
}

func (h *handler) sessionFromCookie(r *http.Request) (*cwmp.ProbeSession, bool) {
	cookie, err := r.Cookie("acs_session")
	if err != nil {
		return nil, false
	}
	return h.store.Get(cookie.Value)
}

func (h *handler) dispatchNext(w http.ResponseWriter, session *cwmp.ProbeSession) {
	body, step, ok := session.NextRequest(cwmp.NewID())
	if !ok {
		h.logger.Info("probe sequence complete", "device", session.DeviceKey)
		h.recordResult(session)
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusOK)
		return
	}

	h.logger.Info("dispatching probe RPC", "device", session.DeviceKey, "step", step)
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// probeRecord is one line of the device-probe-results.jsonl file — the
// machine-readable form of the device compatibility matrix deliverable
// (design doc v3 §14 Phase 0).
type probeRecord struct {
	Timestamp         time.Time         `json:"timestamp"`
	DeviceKey         string            `json:"device_key"`
	Manufacturer      string            `json:"manufacturer"`
	OUI               string            `json:"oui"`
	ProductClass      string            `json:"product_class"`
	SerialNumber      string            `json:"serial_number"`
	EventCodes        []string          `json:"event_codes"`
	RPCMethods        []string          `json:"rpc_methods"`
	Device2Supported  bool              `json:"device2_supported"`
	Device2ParamCount int               `json:"device2_param_count"`
	IGD1Supported     bool              `json:"igd1_supported"`
	IGD1ParamCount    int               `json:"igd1_param_count"`
	Faults            map[string]string `json:"faults,omitempty"`
}

func (h *handler) recordResult(session *cwmp.ProbeSession) {
	deviceID, events, results := session.Snapshot()

	faults := make(map[string]string, len(results.Faults))
	for step, msg := range results.Faults {
		faults[string(step)] = msg
	}

	rec := probeRecord{
		Timestamp:         time.Now().UTC(),
		DeviceKey:         session.DeviceKey,
		Manufacturer:      deviceID.Manufacturer,
		OUI:               deviceID.OUI,
		ProductClass:      deviceID.ProductClass,
		SerialNumber:      deviceID.SerialNumber,
		EventCodes:        events,
		RPCMethods:        results.RPCMethods,
		Device2Supported:  results.Device2Supported,
		Device2ParamCount: results.Device2ParamCount,
		IGD1Supported:     results.IGD1Supported,
		IGD1ParamCount:    results.IGD1ParamCount,
		Faults:            faults,
	}

	line, err := json.Marshal(rec)
	if err != nil {
		h.logger.Error("failed to marshal probe result", "err", err)
		return
	}
	line = append(line, '\n')
	if _, err := h.results.Write(line); err != nil {
		h.logger.Error("failed to write probe result", "err", err)
	}
}
