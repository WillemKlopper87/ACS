// Command acs is the Phase 2 CWMP gateway (build plan §4 Phase 2 / design
// doc v3 §14 Phase 2: "Basic provisioning"). It durably records every
// device and session in Postgres (Phase 1) and now also dispatches
// queued jobs serially within a session — one RPC in flight at a time
// (design doc v3 §5.4 / §19.1) — via internal/jobs and
// internal/parameters. Unlike cmd/probe (Phase 0, throwaway lab tool),
// this is the shape the production gateway keeps growing from.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"acs/internal/auth"
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/jobs"
	"acs/internal/observability"
	"acs/internal/parameters"
	"acs/internal/policy"
	"acs/internal/ratelimit"
	"acs/internal/sessions"
	"acs/internal/store"
	"acs/internal/stun"
	"acs/internal/templates"
)

// maxBodyBytes guards against the "oversized XML" chaos scenario (design
// doc v3 §15.5).
const maxBodyBytes = 4 << 20 // 4 MiB

// Rate limit defaults (build plan §7.4 sub-phase 7c). The per-IP limit is
// coarse defense-in-depth ahead of auth; the per-device limit is the real
// backstop against one misbehaving/compromised CPE, generous enough not
// to trip on a legitimate diagnostics poll loop's several rapid requests
// within one session.
const (
	defaultIPRateLimitPerSecond     = 20
	defaultIPRateLimitBurst         = 40
	defaultDeviceRateLimitPerSecond = 10
	defaultDeviceRateLimitBurst     = 20
	rateLimitIdleTTL                = 10 * time.Minute
)

// remoteIP strips the port from r.RemoteAddr so a client is rate-limited
// per address, not per ephemeral source port.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// defaultDiagPrefix resolves the backward-compatible prefix for a
// diagnostic job queued before jobs.DiagnosticsPingPayload/
// DiagnosticsTraceroutePayload carried their own Prefix field (empty
// Prefix means "assume what this codebase always assumed": TR-181,
// design doc v3 §3's "TR-181 first" convention). Every current caller
// (cmd/api's createDiagnosticsPing/createDiagnosticsTraceroute) resolves
// and stores a real Prefix at job-creation time via
// adapters.DiagnosticsPrefix and the target device's own discovered
// devices.DataModelRoot — this is only the fallback for a job that
// predates that field.
func defaultDiagPrefix(kind string) string {
	return adapters.DiagnosticsPrefix(devices.DataModelRootDevice2, kind)
}

// diagIPPingPollPaths is what a poll (attempts >= 2) reads back: the
// state plus every result parameter TR-181/TR-098 both define for IPPing
// (same leaf names, different subtree root — adapters.DiagnosticsPrefix),
// so the parameter cache ends up populated the moment the diagnostic
// completes rather than needing a second round-trip.
func diagIPPingPollPaths(prefix string) []string {
	return []string{
		prefix + "DiagnosticsState",
		prefix + "SuccessCount",
		prefix + "FailureCount",
		prefix + "AverageResponseTime",
		prefix + "MinimumResponseTime",
		prefix + "MaximumResponseTime",
	}
}

// diagTraceroutePollPaths — see diagIPPingPollPaths. RouteHops.{i}.* is a
// dynamic-length table TR-069 itself defines with no fixed path list to
// poll, so only RouteHopsNumberOfEntries is read back here — an operator
// wanting per-hop detail can follow up with an ordinary GET_PARAMETER
// using that count, the same way this ACS already leaves WiFi
// associated-device detail to GET_PARAMETER rather than a dedicated
// parser.
func diagTraceroutePollPaths(prefix string) []string {
	return []string{
		prefix + "DiagnosticsState",
		prefix + "ResponseTime",
		prefix + "RouteHopsNumberOfEntries",
	}
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelFromEnv()}))

	dsn := os.Getenv("ACS_POSTGRES_DSN")
	if dsn == "" {
		logger.Error("ACS_POSTGRES_DSN is required (e.g. postgres://acs:acs@localhost:5432/acs?sslmode=disable)")
		os.Exit(1)
	}

	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		logger.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := store.Migrate(ctx, db); err != nil {
		logger.Error("failed to apply migrations", "err", err)
		os.Exit(1)
	}
	logger.Info("migrations applied")

	authr := auth.DigestAuthenticator{
		Username:   os.Getenv("ACS_DIGEST_USERNAME"),
		Password:   os.Getenv("ACS_DIGEST_PASSWORD"),
		AllowBasic: envBool("ACS_AUTH_ALLOW_BASIC"),
	}
	if !authr.Enabled() {
		logger.Warn("ACS_DIGEST_USERNAME/ACS_DIGEST_PASSWORD not set — CWMP endpoint is running WITHOUT authentication (design doc v3 §11.1/§11.2).")
	}
	if authr.AllowBasic {
		logger.Info("HTTP Basic fallback enabled for CWMP auth (ACS_AUTH_ALLOW_BASIC) — use only with TLS in production")
	}

	metrics := observability.NewMetrics("acs")

	ipRate := envOrFloat("ACS_RATE_LIMIT_IP_PER_SECOND", defaultIPRateLimitPerSecond)
	ipBurst := envOrInt("ACS_RATE_LIMIT_IP_BURST", defaultIPRateLimitBurst)
	deviceRate := envOrFloat("ACS_RATE_LIMIT_DEVICE_PER_SECOND", defaultDeviceRateLimitPerSecond)
	deviceBurst := envOrInt("ACS_RATE_LIMIT_DEVICE_BURST", defaultDeviceRateLimitBurst)

	h := &handler{
		logger:        logger,
		auth:          authr,
		devices:       devices.NewRepository(db),
		sessions:      sessions.NewRepository(db),
		jobs:          jobs.NewRepository(db),
		params:        parameters.NewRepository(db),
		auditor:       observability.NewAuditor(db),
		metrics:       metrics,
		policies:      policy.NewRepository(db),
		templates:     templates.NewRepository(db),
		ipLimiter:     ratelimit.New(ipRate, ipBurst, rateLimitIdleTTL),
		deviceLimiter: ratelimit.New(deviceRate, deviceBurst, rateLimitIdleTTL),
	}
	logger.Info("rate limits configured", "ip_per_second", ipRate, "ip_burst", ipBurst,
		"device_per_second", deviceRate, "device_burst", deviceBurst)

	go pollDevicesOnlineGauge(ctx, h.devices, metrics, logger)

	mux := http.NewServeMux()
	// Catch-all rather than exact "/cwmp": CPEs in the field get provisioned
	// with ACS URLs like "http://host:7547/", "/cwmp/", or "/acs", and a 404
	// on the path mismatch looks to the operator exactly like "device won't
	// connect". Any POST that reaches this server is treated as CWMP;
	// handleCWMP logs the path so misconfigured device URLs stay visible.
	mux.HandleFunc("/", h.handleCWMP)
	mux.Handle("GET /metrics", metrics.Handler())

	addr := envOr("ACS_ADDR", ":7547")
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
		// CPE HTTP stacks are slow and often on lossy last-mile links —
		// generous per-request timeouts, but never unbounded, so a stalled
		// connection can't hold a socket forever. IdleTimeout covers the
		// keep-alive gap between envelopes within a CWMP session.
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       5 * time.Minute,
	}

	certFile := os.Getenv("ACS_TLS_CERT")
	keyFile := os.Getenv("ACS_TLS_KEY")

	// TLS compatibility floor. Go 1.22+ raised the crypto/tls *server*
	// default minimum to TLS 1.2 and removed RSA-key-exchange cipher
	// suites from the defaults — but a large share of deployed CPE TLS
	// stacks (older Broadcom/Econet SDK firmwares) top out at TLS 1.0/1.1
	// with RSA-kex CBC suites, and those devices silently fail the
	// handshake and never appear in the ACS logs. Default here is a
	// permissive TLS 1.0 floor with the legacy suites explicitly enabled,
	// which is standard practice for a public CWMP endpoint; set
	// ACS_TLS_MIN_VERSION=1.2 to harden once the fleet is known-modern.
	tlsCfg := &tls.Config{}
	switch v := envOr("ACS_TLS_MIN_VERSION", "1.0"); v {
	case "1.0":
		tlsCfg.MinVersion = tls.VersionTLS10
	case "1.1":
		tlsCfg.MinVersion = tls.VersionTLS11
	case "1.2":
		tlsCfg.MinVersion = tls.VersionTLS12
	case "1.3":
		tlsCfg.MinVersion = tls.VersionTLS13
	default:
		logger.Error("invalid ACS_TLS_MIN_VERSION (want 1.0, 1.1, 1.2 or 1.3)", "value", v)
		os.Exit(1)
	}
	if tlsCfg.MinVersion < tls.VersionTLS12 {
		// Explicit list (applies to TLS 1.0–1.2 only; 1.3 suites are not
		// configurable): modern ECDHE/AEAD suites first, then the legacy
		// CBC and RSA-kex suites old CPEs need. Explicitly configuring
		// them re-enables what Go's defaults dropped without needing
		// GODEBUG=tlsrsakex=1.
		tlsCfg.CipherSuites = []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
		}
	}

	// mTLS (design doc v3 §11.2: "preferred: mutual TLS, fallback: HTTP
	// Digest ... must support both"). Optional and additive to Digest, not
	// a replacement for it — ClientAuth: VerifyClientCertIfGiven means the
	// server always *offers* to receive a client cert but never refuses a
	// handshake for lacking one, so devices that can't do mTLS yet
	// (prerequisite P3 unresolved per-vendor, v3 §13) keep working over
	// Digest on the exact same endpoint. Any cert that IS presented is
	// chain-verified against ACS_MTLS_CA_CERT before the handshake
	// completes — handleCWMP only ever sees PeerCertificates that already
	// passed that check.
	if caCertFile := os.Getenv("ACS_MTLS_CA_CERT"); caCertFile != "" {
		caCert, err := os.ReadFile(caCertFile)
		if err != nil {
			logger.Error("failed to read ACS_MTLS_CA_CERT", "err", err, "path", caCertFile)
			os.Exit(1)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			logger.Error("ACS_MTLS_CA_CERT did not contain a valid PEM certificate", "path", caCertFile)
			os.Exit(1)
		}
		tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven
		tlsCfg.ClientCAs = pool
		logger.Info("mTLS enabled — client certs verified against ACS_MTLS_CA_CERT when presented, Digest fallback still accepted", "ca_cert", caCertFile)
	}
	server.TLSConfig = tlsCfg

	ctx2, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// STUN server (critical feature backlog: NAT traversal). A CPE behind
	// CGNAT points its own ManagementServer.STUNServerAddress/Port at this
	// — set ACS_STUN_ADDR="off" to disable if a separate/existing STUN
	// infrastructure is preferred instead.
	stunAddr := envOr("ACS_STUN_ADDR", ":3478")
	if stunAddr != "off" {
		stunServer, err := stun.Listen(stunAddr, logger)
		if err != nil {
			logger.Error("failed to start stun server", "err", err, "addr", stunAddr)
			os.Exit(1)
		}
		logger.Info("stun server listening", "addr", stunAddr)
		go stunServer.Run(ctx2)
	} else {
		logger.Info("stun server disabled (ACS_STUN_ADDR=off)")
	}

	errCh := make(chan error, 1)
	go func() {
		if certFile != "" && keyFile != "" {
			logger.Info("CWMP gateway listening (TLS)", "addr", addr, "path", "/cwmp")
			errCh <- server.ListenAndServeTLS(certFile, keyFile)
		} else {
			logger.Warn("CWMP gateway listening WITHOUT TLS — design doc v3 §11.1 requires HTTPS-only in production", "addr", addr, "path", "/cwmp")
			errCh <- server.ListenAndServe()
		}
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	case <-ctx2.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrFloat(key string, fallback float64) float64 {
	v, err := strconv.ParseFloat(os.Getenv(key), 64)
	if err != nil {
		return fallback
	}
	return v
}

func envBool(key string) bool {
	switch os.Getenv(key) {
	case "1", "true", "TRUE", "yes", "on":
		return true
	}
	return false
}

func envOrInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func levelFromEnv() slog.Level {
	if os.Getenv("ACS_DEBUG") != "" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

type handler struct {
	logger    *slog.Logger
	auth      auth.DigestAuthenticator
	devices   *devices.Repository
	sessions  *sessions.Repository
	jobs      *jobs.Repository
	params    *parameters.Repository
	auditor   *observability.Auditor
	metrics   *observability.Metrics
	policies  *policy.Repository
	templates *templates.Repository

	ipLimiter     *ratelimit.Limiter
	deviceLimiter *ratelimit.Limiter
}

// devicesOnlinePollInterval is how often the acs_devices_online gauge is
// refreshed from Postgres — a periodic poll rather than an update on
// every Inform/session-close, so a Prometheus scrape never triggers an
// extra query and the hot CWMP request path stays untouched by metrics
// bookkeeping beyond simple counter increments.
const devicesOnlinePollInterval = 15 * time.Second

func pollDevicesOnlineGauge(ctx context.Context, repo *devices.Repository, metrics *observability.Metrics, logger *slog.Logger) {
	ticker := time.NewTicker(devicesOnlinePollInterval)
	defer ticker.Stop()
	for {
		counts, err := repo.CountByOnlineStatus(ctx, nil, false)
		if err != nil {
			logger.Error("failed to refresh devices-online gauge", "err", err)
		} else {
			for _, status := range []string{"ONLINE", "OFFLINE", "UNREACHABLE"} {
				metrics.DevicesOnline.WithLabelValues(status).Set(float64(counts[status]))
			}
		}
		<-ticker.C
	}
}

// handleCWMP implements the Phase 2 session shape:
//
//	Inform            -> upsert device, open session, InformResponse
//	anything else     -> complete any in-flight job's response, then
//	                     dispatch the next queued job or close the session
func (h *handler) handleCWMP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "CWMP requires POST", http.StatusMethodNotAllowed)
		return
	}

	// Coarse per-IP limit, ahead of auth and body parsing entirely — the
	// cheapest possible rejection for an unauthenticated flood (build
	// plan §7.1/§7c). A misbehaving *authenticated* device is caught
	// below, per-device, after we know who it is.
	if !h.ipLimiter.Allow(remoteIP(r)) {
		h.metrics.RateLimitRejectedTotal.Inc()
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	if r.URL.Path != "/cwmp" {
		h.logger.Debug("CWMP POST on non-standard path (device ACS URL differs from /cwmp)", "path", r.URL.Path, "remote", r.RemoteAddr)
	}

	// A presented client cert has already been chain-verified by the TLS
	// handshake itself (server.TLSConfig's ClientCAs, set only when
	// ACS_MTLS_CA_CERT is configured) — mTLS is the *preferred* method
	// per v3 §11.2, so it supersedes Digest for this request rather than
	// requiring both.
	authMode := devices.AuthModeNone
	mtlsAuthenticated := r.TLS != nil && len(r.TLS.PeerCertificates) > 0
	if mtlsAuthenticated {
		authMode = devices.AuthModeMTLS
	} else if h.auth.Enabled() {
		if !h.auth.Verify(r) {
			h.logger.Warn("authentication failed or missing", "remote", r.RemoteAddr)
			// Drain the unauthenticated request body before writing the
			// 401 so the keep-alive connection survives: an Inform can
			// exceed the amount net/http is willing to auto-discard, and
			// a closed connection here makes some CPEs treat the whole
			// session as failed instead of retrying with credentials.
			_, _ = io.Copy(io.Discard, r.Body)
			h.auth.Challenge(w)
			return
		}
		authMode = devices.AuthModeDigest
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.Warn("failed to read request body (possibly oversized)", "err", err, "remote", r.RemoteAddr)
		http.Error(w, "body too large or unreadable", http.StatusBadRequest)
		return
	}

	env, err := cwmp.ParseEnvelope(raw)
	if err != nil {
		h.logger.Error("malformed SOAP/XML", "err", err, "remote", r.RemoteAddr, "body", string(raw))
		http.Error(w, "malformed XML", http.StatusBadRequest)
		return
	}

	// Per-device limit, keyed on whatever identifies the device at this
	// point in the exchange: the natural key on Inform (before a session
	// cookie exists), the session cookie for every request after that —
	// which is 1:1 with a device for that session's lifetime, so it's
	// deviceID in effect without a DB lookup on every request just to
	// rate-limit. Generous burst (see defaultDeviceRateLimitBurst) so a
	// legitimate diagnostics poll loop (Phase 5) — several requests in
	// quick succession within one session — isn't mistaken for abuse.
	deviceKey := remoteIP(r)
	if env.Body.Inform != nil {
		deviceKey = env.Body.Inform.DeviceId.NaturalKey()
	} else if cookie, err := r.Cookie("acs_session"); err == nil {
		deviceKey = cookie.Value
	}
	if !h.deviceLimiter.Allow(deviceKey) {
		h.metrics.RateLimitRejectedTotal.Inc()
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	ctx := r.Context()

	// Responses to CPE-initiated RPCs must echo the request's cwmp:ID
	// (TR-069 §3.4.1.1) and speak the same CWMP namespace version the CPE
	// used — strict CPE stacks abort the session on either mismatch.
	respID := env.Header.ID
	if respID == "" {
		respID = cwmp.NewID()
	}
	ns := cwmp.DetectCWMPNamespace(raw)

	if env.Body.Inform != nil {
		h.handleInform(ctx, w, env.Body.Inform, authMode, respID, ns)
		return
	}

	// TransferComplete is checked ahead of the normal session-dispatch
	// path because it doesn't correlate to session.CurrentJobID at all —
	// the CPE may not send it until a session well after the one that
	// dispatched Download (fetch, flash, and reboot can take minutes),
	// so the only thing tying it back to a job is the CommandKey inside
	// the message itself (build plan §4 Phase 4).
	if env.Body.TransferComplete != nil {
		h.handleTransferComplete(ctx, w, env.Body.TransferComplete, respID, ns)
		return
	}

	h.dispatch(ctx, w, r, env.Body)
}

func (h *handler) handleInform(ctx context.Context, w http.ResponseWriter, inform *cwmp.Inform, authMode, respID, ns string) {
	h.metrics.InformsTotal.Inc()
	events := inform.EventCodes()

	device, err := h.devices.UpsertFromInform(ctx, inform.DeviceId, events)
	if err != nil {
		h.logger.Error("failed to upsert device", "err", err, "oui_serial", inform.DeviceId.NaturalKey())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.devices.UpdateAuthMode(ctx, device.ID, authMode); err != nil {
		h.logger.Error("failed to record device auth mode", "err", err, "device_id", device.ID)
	}

	if len(inform.ParameterList) > 0 {
		h.cacheParameterValues(ctx, device.ID, inform.ParameterList, parameters.SourceInform)

		if url := extractConnectionRequestURL(inform.ParameterList); url != "" {
			if err := h.devices.UpdateConnectionRequestURL(ctx, device.ID, url); err != nil {
				h.logger.Error("failed to update connection request url", "err", err, "device_id", device.ID)
			}
		}

		if addr, natDetected, ok := extractSTUNStatus(inform.ParameterList); ok {
			if err := h.devices.UpdateSTUNStatus(ctx, device.ID, addr, natDetected); err != nil {
				h.logger.Error("failed to update stun status", "err", err, "device_id", device.ID)
			}
		}
	}

	h.correlateValueChangeEvents(ctx, device, inform.Event, inform.ParameterList)
	h.enforcePolicies(ctx, device, inform.ParameterList)

	if inform.HasEventCode("0") {
		h.applyAutoProvisioningTemplates(ctx, device)
		h.autoDiscoverParameters(ctx, device)
	}

	if inform.HasEventCode("6") {
		if err := h.devices.MarkInformedAfterConnectionRequest(ctx, device.ID); err != nil {
			h.logger.Error("failed to mark informed-after-connection-request", "err", err, "device_id", device.ID)
		}
	}

	session, err := h.sessions.Open(ctx, device.ID, events)
	if err != nil {
		h.logger.Error("failed to open session", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.metrics.SessionsOpenedTotal.Inc()

	if err := h.auditor.Record(ctx, "system", device.ID, "Inform", map[string]any{
		"event_codes": events,
		"session_id":  session.ID,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	h.logger.Info("Inform received",
		"device_id", device.ID,
		"oui_serial", device.OUISerial,
		"manufacturer", device.Manufacturer,
		"events", events,
		"session_id", session.ID,
	)

	http.SetCookie(w, &http.Cookie{
		Name:     "acs_session",
		Value:    session.ID,
		Path:     "/",
		HttpOnly: true,
	})
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cwmp.RenderInformResponseNS(respID, ns))
}

// dispatch handles every non-Inform POST on an open session: if a job's
// RPC was already in flight, complete it with this response/fault; then
// try to lease the next queued job for the device and send its RPC, or
// close the session if there's no more work (design doc v3 §5.4).
func (h *handler) dispatch(ctx context.Context, w http.ResponseWriter, r *http.Request, body cwmp.InboundBody) {
	cookie, err := r.Cookie("acs_session")
	if err != nil {
		h.logger.Warn("request outside a known session (missing session cookie); nothing to do", "remote", r.RemoteAddr)
		h.respondEmpty(w)
		return
	}

	session, err := h.sessions.Get(ctx, cookie.Value)
	if err != nil {
		h.logger.Warn("request for unknown session", "session_id", cookie.Value, "remote", r.RemoteAddr)
		h.respondEmpty(w)
		return
	}
	if session.IsClosed() {
		h.logger.Warn("request for already-closed session", "session_id", cookie.Value, "remote", r.RemoteAddr)
		h.respondEmpty(w)
		return
	}

	if session.CurrentJobID != nil {
		h.completeJob(ctx, session.DeviceID, *session.CurrentJobID, body)
		if err := h.sessions.SetCurrentJob(ctx, session.ID, ""); err != nil {
			h.logger.Error("failed to clear in-flight job", "err", err, "session_id", session.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	job, err := h.jobs.Lease(ctx, session.DeviceID)
	if err != nil {
		h.logger.Error("failed to lease next job", "err", err, "device_id", session.DeviceID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if job == nil {
		h.closeSession(ctx, w, session.ID, session.DeviceID)
		return
	}

	if err := h.sessions.SetCurrentJob(ctx, session.ID, job.ID); err != nil {
		h.logger.Error("failed to record in-flight job", "err", err, "session_id", session.ID, "job_id", job.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	requestBody, ok := h.renderJobRequest(job)
	if !ok {
		h.logger.Error("job has unrenderable payload; failing it", "job_id", job.ID, "type", job.Type)
		if err := h.jobs.MarkFailed(ctx, job.ID, "", "unrenderable job payload"); err != nil {
			h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
		}
		h.respondEmpty(w)
		return
	}

	h.logger.Info("dispatching job RPC", "job_id", job.ID, "command_key", job.CommandKey,
		"type", job.Type, "device_id", session.DeviceID, "session_id", session.ID)

	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(requestBody)
}

// renderJobRequest turns a leased job into the CWMP RPC request bytes to
// send the CPE.
func (h *handler) renderJobRequest(job *jobs.Job) (body []byte, ok bool) {
	id := cwmp.NewID()
	switch job.Type {
	case jobs.TypeSetParameter:
		var payload jobs.SetParameterPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		params := make([]cwmp.ParameterValueStruct, len(payload.Parameters))
		for i, p := range payload.Parameters {
			params[i] = cwmp.ParameterValueStruct{Name: p.Name, Value: p.Value}
		}
		return cwmp.RenderSetParameterValues(id, params, job.CommandKey), true

	case jobs.TypeGetParameter:
		var payload jobs.GetParameterPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderGetParameterValues(id, payload.Paths), true

	case jobs.TypeFirmwareDownload:
		var payload jobs.FirmwareDownloadPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderDownload(id, job.CommandKey, payload.FileType, payload.URL,
			payload.Username, payload.Password, payload.FileSize, payload.TargetFilename, payload.DelaySeconds), true

	case jobs.TypeDiagnosticsPing:
		var payload jobs.DiagnosticsPingPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		prefix := payload.Prefix
		if prefix == "" {
			prefix = defaultDiagPrefix(adapters.DiagnosticPing)
		}
		if job.Attempts <= 1 {
			// Trigger: TR-069 diagnostics aren't a dedicated RPC — the
			// ACS writes the diagnostic's input parameters and kicks it
			// off via an ordinary SetParameterValues with
			// DiagnosticsState=Requested (v3 §10.1). Later attempts poll
			// instead (see below); attempts counts from Lease, so 1 means
			// this is the very first dispatch.
			params := []cwmp.ParameterValueStruct{
				{Name: prefix + "Host", Value: payload.Host},
				{Name: prefix + "NumberOfRepetitions", Value: strconv.Itoa(payload.NumberOfRepetitions)},
				{Name: prefix + "Timeout", Value: strconv.Itoa(payload.Timeout)},
				{Name: prefix + "DataBlockSize", Value: strconv.Itoa(payload.DataBlockSize)},
				{Name: prefix + "DSCP", Value: strconv.Itoa(payload.DSCP)},
				{Name: prefix + "DiagnosticsState", Value: "Requested"},
			}
			return cwmp.RenderSetParameterValues(id, params, job.CommandKey), true
		}
		// Poll: read DiagnosticsState (and the result parameters, so a
		// completed poll needs no further round-trip) until it leaves
		// "Requested".
		return cwmp.RenderGetParameterValues(id, diagIPPingPollPaths(prefix)), true

	case jobs.TypeDiagnosticsTraceroute:
		var payload jobs.DiagnosticsTraceroutePayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		prefix := payload.Prefix
		if prefix == "" {
			prefix = defaultDiagPrefix(adapters.DiagnosticTraceroute)
		}
		if job.Attempts <= 1 {
			params := []cwmp.ParameterValueStruct{
				{Name: prefix + "Host", Value: payload.Host},
				{Name: prefix + "NumberOfTries", Value: strconv.Itoa(payload.NumberOfTries)},
				{Name: prefix + "Timeout", Value: strconv.Itoa(payload.Timeout)},
				{Name: prefix + "DataBlockSize", Value: strconv.Itoa(payload.DataBlockSize)},
				{Name: prefix + "DSCP", Value: strconv.Itoa(payload.DSCP)},
				{Name: prefix + "MaxHopCount", Value: strconv.Itoa(payload.MaxHopCount)},
				{Name: prefix + "DiagnosticsState", Value: "Requested"},
			}
			return cwmp.RenderSetParameterValues(id, params, job.CommandKey), true
		}
		return cwmp.RenderGetParameterValues(id, diagTraceroutePollPaths(prefix)), true

	case jobs.TypeAddObject:
		var payload jobs.AddObjectPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderAddObject(id, payload.ObjectPath, job.CommandKey), true

	case jobs.TypeDeleteObject:
		var payload jobs.DeleteObjectPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderDeleteObject(id, payload.ObjectPath, job.CommandKey), true

	case jobs.TypeReboot:
		return cwmp.RenderReboot(id, job.CommandKey), true

	case jobs.TypeFactoryReset:
		return cwmp.RenderFactoryReset(id), true

	case jobs.TypeScheduleInform:
		var payload jobs.ScheduleInformPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderScheduleInform(id, job.CommandKey, payload.DelaySeconds), true

	case jobs.TypeSetParameterAttributes:
		var payload jobs.SetParameterAttributesPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		attrs := make([]cwmp.AttributeWrite, len(payload.Attributes))
		for i, a := range payload.Attributes {
			attrs[i] = cwmp.AttributeWrite{Name: a.Name, Notification: a.Notification}
		}
		return cwmp.RenderSetParameterAttributes(id, attrs), true

	case jobs.TypeGetParameterAttributes:
		var payload jobs.GetParameterAttributesPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderGetParameterAttributes(id, payload.Paths), true

	case jobs.TypeUpload:
		var payload jobs.UploadPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderUpload(id, job.CommandKey, payload.FileType, payload.URL, payload.Username, payload.Password, payload.DelaySeconds), true

	case jobs.TypeParameterDiscovery:
		var payload jobs.ParameterDiscoveryPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, false
		}
		return cwmp.RenderGetParameterNames(id, payload.Root, false), true

	default:
		return nil, false
	}
}

// handleTransferComplete acks the CPE's TransferComplete RPC and
// completes whichever job its CommandKey names — found by CommandKey,
// not by any session state, since this may be a session that never
// dispatched anything itself (build plan §4 Phase 4).
func (h *handler) handleTransferComplete(ctx context.Context, w http.ResponseWriter, tc *cwmp.TransferComplete, respID, ns string) {
	job, err := h.jobs.ByCommandKey(ctx, tc.CommandKey)
	if err != nil {
		h.logger.Warn("TransferComplete for unrecognized command_key", "command_key", tc.CommandKey, "err", err)
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cwmp.RenderTransferCompleteResponseNS(respID, ns))
		return
	}

	if tc.IsFault() {
		if err := h.jobs.MarkFailed(ctx, job.ID, tc.FaultStruct.FaultCode, tc.FaultStruct.FaultString); err != nil {
			h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
		}
		h.auditFailure(ctx, job.DeviceID, job, tc.FaultStruct.FaultCode, tc.FaultStruct.FaultString)
	} else {
		h.markJobSuccess(ctx, job.DeviceID, job)

		// TransferComplete is shared between FIRMWARE_DOWNLOAD and UPLOAD
		// (both are CPE-driven async transfers, TR-069 §9.2/§A.3.2.7) —
		// only firmware has a SoftwareVersion worth confirming afterward
		// (v3 §9.3: don't just trust the ack). An Upload's real
		// confirmation is the file itself landing at the receipt endpoint,
		// handled separately by cmd/api's upload-receive handler.
		if job.Type == jobs.TypeFirmwareDownload {
			confirm, err := h.jobs.Create(ctx, job.DeviceID, jobs.TypeGetParameter,
				jobs.GetParameterPayload{Paths: []string{"Device.DeviceInfo.SoftwareVersion"}}, "system:confirm")
			if err != nil {
				h.logger.Error("failed to queue post-firmware version confirmation", "err", err, "device_id", job.DeviceID)
			} else {
				h.metrics.JobsCreatedTotal.WithLabelValues(jobs.TypeGetParameter).Inc()
				h.logger.Info("queued post-firmware version confirmation", "job_id", confirm.ID, "command_key", confirm.CommandKey, "device_id", job.DeviceID)
			}
		}
	}

	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cwmp.RenderTransferCompleteResponseNS(respID, ns))
}

// completeJob records the CPE's response (or fault) against whichever job
// was in flight, updates the parameter cache when applicable, and — on a
// successful SET_PARAMETER — queues a GET_PARAMETER confirmation job so
// the cache ends up reflecting what the CPE actually reports rather than
// what the ACS assumed was applied (the same "don't trust the ack, verify
// afterwards" pattern v3 §9.3 uses for TransferComplete -> SoftwareVersion,
// applied here to ordinary parameter writes).
func (h *handler) completeJob(ctx context.Context, deviceID, jobID string, body cwmp.InboundBody) {
	job, err := h.jobs.ByID(ctx, jobID)
	if err != nil {
		h.logger.Error("failed to load in-flight job", "err", err, "job_id", jobID)
		return
	}

	// PARAMETER_DISCOVERY handles its own fault path (a fault or an empty
	// name list both mean "wrong root, try the other one" — see
	// completeParameterDiscovery) instead of the generic fault-means-FAILED
	// handling below, so it's special-cased ahead of that check.
	if job.Type == jobs.TypeParameterDiscovery {
		h.completeParameterDiscovery(ctx, deviceID, job, body)
		return
	}

	if body.Fault != nil {
		code, msg := body.Fault.CWMPCode(), body.Fault.CWMPMessage()
		if err := h.jobs.MarkFailed(ctx, job.ID, code, msg); err != nil {
			h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
		}
		h.auditFailure(ctx, deviceID, job, code, msg)
		return
	}

	switch job.Type {
	case jobs.TypeSetParameter:
		if body.SetParameterValuesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)

		var payload jobs.SetParameterPayload
		if err := json.Unmarshal(job.Payload, &payload); err == nil && len(payload.Parameters) > 0 {
			values := make(map[string]parameters.CachedValue, len(payload.Parameters))
			paths := make([]string, len(payload.Parameters))
			for i, p := range payload.Parameters {
				values[p.Name] = parameters.CachedValue{Value: p.Value, Type: p.Type, UpdatedAt: time.Now().UTC(), Source: parameters.SourceSetValues}
				paths[i] = p.Name
			}
			if err := h.params.Upsert(ctx, deviceID, values); err != nil {
				h.logger.Error("failed to upsert parameter cache", "err", err, "device_id", deviceID)
			}

			confirm, err := h.jobs.Create(ctx, deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, "system:confirm")
			if err != nil {
				h.logger.Error("failed to queue confirmation read", "err", err, "device_id", deviceID)
			} else {
				h.metrics.JobsCreatedTotal.WithLabelValues(jobs.TypeGetParameter).Inc()
				h.logger.Info("queued confirmation read", "job_id", confirm.ID, "command_key", confirm.CommandKey, "device_id", deviceID)
			}
		}

	case jobs.TypeGetParameter:
		if body.GetParameterValuesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)
		h.cacheParameterValues(ctx, deviceID, body.GetParameterValuesResponse.ParameterList, parameters.SourceGetValues)

	case jobs.TypeFirmwareDownload:
		if body.DownloadResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		// Not SUCCESS: DownloadResponse only means the CPE accepted the
		// request (v3 §9.2/§19.7) — the real outcome arrives later as
		// TransferComplete, handled by handleTransferComplete above,
		// correlated by CommandKey rather than this session.
		if err := h.jobs.MarkAwaitingTransferComplete(ctx, job.ID); err != nil {
			h.logger.Error("failed to mark job awaiting transfer complete", "err", err, "job_id", job.ID)
		}
		h.logger.Info("firmware download accepted, awaiting transfer complete",
			"job_id", job.ID, "command_key", job.CommandKey, "device_id", deviceID, "status", body.DownloadResponse.Status)

	case jobs.TypeUpload:
		if body.UploadResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		// Same "accepted now, TransferComplete confirms later" shape as
		// FIRMWARE_DOWNLOAD — the actual file arrives at cmd/api's
		// upload-receive endpoint independently of this session.
		if err := h.jobs.MarkAwaitingTransferComplete(ctx, job.ID); err != nil {
			h.logger.Error("failed to mark job awaiting transfer complete", "err", err, "job_id", job.ID)
		}
		h.logger.Info("upload accepted, awaiting transfer complete",
			"job_id", job.ID, "command_key", job.CommandKey, "device_id", deviceID, "status", body.UploadResponse.Status)

	case jobs.TypeDiagnosticsPing:
		if job.Attempts <= 1 {
			// The trigger (SetParameterValues) was acked — the CPE has
			// accepted the request, not run it yet. Requeue so the next
			// dispatch on this device polls instead of re-triggering.
			if body.SetParameterValuesResponse == nil {
				h.markUnexpectedResponse(ctx, deviceID, job)
				return
			}
			h.requeueDiagnostic(ctx, deviceID, job, "triggered, awaiting result")
			return
		}

		// A poll (GetParameterValues) came back.
		if body.GetParameterValuesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		list := body.GetParameterValuesResponse.ParameterList
		h.cacheParameterValues(ctx, deviceID, list, parameters.SourceGetValues)

		var pingPayload jobs.DiagnosticsPingPayload
		if err := json.Unmarshal(job.Payload, &pingPayload); err != nil {
			h.logger.Error("failed to unmarshal diagnostics ping payload; assuming TR-181", "err", err, "job_id", job.ID)
		}
		pingPrefix := pingPayload.Prefix
		if pingPrefix == "" {
			pingPrefix = defaultDiagPrefix(adapters.DiagnosticPing)
		}

		switch state := diagnosticsState(list, pingPrefix); state {
		case "Requested", "":
			// Still running (or the CPE hasn't populated the state param
			// yet) — poll again.
			h.requeueDiagnostic(ctx, deviceID, job, "still running")
		case "Complete":
			h.markJobSuccess(ctx, deviceID, job)
		default:
			// TR-181 error states, e.g. Error_CannotResolveHostName,
			// Error_Internal, Error_Other.
			if err := h.jobs.MarkFailed(ctx, job.ID, "", state); err != nil {
				h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
			}
			h.auditFailure(ctx, deviceID, job, "", state)
		}

	case jobs.TypeDiagnosticsTraceroute:
		if job.Attempts <= 1 {
			if body.SetParameterValuesResponse == nil {
				h.markUnexpectedResponse(ctx, deviceID, job)
				return
			}
			h.requeueDiagnostic(ctx, deviceID, job, "triggered, awaiting result")
			return
		}

		if body.GetParameterValuesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		list := body.GetParameterValuesResponse.ParameterList
		h.cacheParameterValues(ctx, deviceID, list, parameters.SourceGetValues)

		var tracePayload jobs.DiagnosticsTraceroutePayload
		if err := json.Unmarshal(job.Payload, &tracePayload); err != nil {
			h.logger.Error("failed to unmarshal diagnostics traceroute payload; assuming TR-181", "err", err, "job_id", job.ID)
		}
		tracePrefix := tracePayload.Prefix
		if tracePrefix == "" {
			tracePrefix = defaultDiagPrefix(adapters.DiagnosticTraceroute)
		}

		switch state := diagnosticsState(list, tracePrefix); state {
		case "Requested", "":
			h.requeueDiagnostic(ctx, deviceID, job, "still running")
		case "Complete":
			h.markJobSuccess(ctx, deviceID, job)
		default:
			if err := h.jobs.MarkFailed(ctx, job.ID, "", state); err != nil {
				h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
			}
			h.auditFailure(ctx, deviceID, job, "", state)
		}

	case jobs.TypeAddObject:
		if body.AddObjectResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		if err := h.jobs.MarkSuccessWithDetail(ctx, job.ID, map[string]any{"instance_number": body.AddObjectResponse.InstanceNumber}); err != nil {
			h.logger.Error("failed to mark job success", "err", err, "job_id", job.ID)
		}
		h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusSuccess).Inc()
		if err := h.auditor.Record(ctx, "system", deviceID, "JobSucceeded", map[string]any{
			"job_id": job.ID, "command_key": job.CommandKey, "type": job.Type, "instance_number": body.AddObjectResponse.InstanceNumber,
		}); err != nil {
			h.logger.Error("failed to write audit record", "err", err)
		}
		h.logger.Info("job succeeded", "job_id", job.ID, "command_key", job.CommandKey, "type", job.Type, "device_id", deviceID, "instance_number", body.AddObjectResponse.InstanceNumber)

	case jobs.TypeDeleteObject:
		if body.DeleteObjectResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)

	case jobs.TypeReboot:
		if body.RebootResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		// The CPE accepted the reboot request — the real confirmation is
		// the fresh Inform that follows once it comes back up (event code
		// "M Reboot"), the same "accepted now, confirmed later" shape
		// Download/TransferComplete already established. There's no
		// further RPC to correlate that Inform against; the job is done
		// once the CPE has acknowledged it will reboot.
		h.markJobSuccess(ctx, deviceID, job)

	case jobs.TypeFactoryReset:
		if body.FactoryResetResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)

	case jobs.TypeScheduleInform:
		if body.ScheduleInformResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)

	case jobs.TypeSetParameterAttributes:
		if body.SetParameterAttributesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		h.markJobSuccess(ctx, deviceID, job)

	case jobs.TypeGetParameterAttributes:
		if body.GetParameterAttributesResponse == nil {
			h.markUnexpectedResponse(ctx, deviceID, job)
			return
		}
		detail := make(map[string]any, len(body.GetParameterAttributesResponse.ParameterList))
		for _, a := range body.GetParameterAttributesResponse.ParameterList {
			detail[a.Name] = map[string]any{"notification": a.Notification, "access_list": a.AccessList}
		}
		if err := h.jobs.MarkSuccessWithDetail(ctx, job.ID, detail); err != nil {
			h.logger.Error("failed to mark job success", "err", err, "job_id", job.ID)
		}
		h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusSuccess).Inc()

	default:
		h.logger.Error("completed job has unknown type", "job_id", job.ID, "type", job.Type)
	}
}

func (h *handler) markJobSuccess(ctx context.Context, deviceID string, job *jobs.Job) {
	if err := h.jobs.MarkSuccess(ctx, job.ID); err != nil {
		h.logger.Error("failed to mark job success", "err", err, "job_id", job.ID)
	}
	h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusSuccess).Inc()
	if err := h.auditor.Record(ctx, "system", deviceID, "JobSucceeded", map[string]any{
		"job_id": job.ID, "command_key": job.CommandKey, "type": job.Type,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("job succeeded", "job_id", job.ID, "command_key", job.CommandKey, "type", job.Type, "device_id", deviceID)
}

func (h *handler) markUnexpectedResponse(ctx context.Context, deviceID string, job *jobs.Job) {
	const msg = "response did not match the RPC that was sent"
	if err := h.jobs.MarkFailed(ctx, job.ID, "", msg); err != nil {
		h.logger.Error("failed to mark job failed", "err", err, "job_id", job.ID)
	}
	h.auditFailure(ctx, deviceID, job, "", msg)
}

// requeueDiagnostic cycles a DIAGNOSTICS_PING job back to QUEUED for
// another poll, unless it has exhausted max_attempts — without a cap, a
// device whose DiagnosticsState never leaves "Requested" would poll
// forever (build plan §4 Phase 5).
func (h *handler) requeueDiagnostic(ctx context.Context, deviceID string, job *jobs.Job, detail string) {
	if job.Attempts >= job.MaxAttempts {
		msg := "diagnostic did not complete within " + strconv.Itoa(job.MaxAttempts) + " attempts"
		if err := h.jobs.MarkTimeout(ctx, job.ID, msg); err != nil {
			h.logger.Error("failed to mark job timeout", "err", err, "job_id", job.ID)
		}
		h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusTimeout).Inc()
		h.logger.Warn("diagnostic exhausted max attempts", "job_id", job.ID, "command_key", job.CommandKey,
			"device_id", deviceID, "attempts", job.Attempts, "max_attempts", job.MaxAttempts)
		return
	}
	if err := h.jobs.Requeue(ctx, job.ID); err != nil {
		h.logger.Error("failed to requeue diagnostic job", "err", err, "job_id", job.ID)
		return
	}
	h.logger.Info("diagnostic requeued for poll", "job_id", job.ID, "command_key", job.CommandKey,
		"device_id", deviceID, "attempts", job.Attempts, "detail", detail)
}

// diagnosticsState reads a diagnostic's DiagnosticsState parameter out of
// a GetParameterValues response (prefix selects IPPing vs TraceRoute's
// subtree), or "" if the CPE didn't include it.
func diagnosticsState(list []cwmp.ParameterValueStruct, prefix string) string {
	for _, p := range list {
		if p.Name == prefix+"DiagnosticsState" {
			return p.Value
		}
	}
	return ""
}

func (h *handler) auditFailure(ctx context.Context, deviceID string, job *jobs.Job, code, msg string) {
	h.metrics.JobsCompletedTotal.WithLabelValues(job.Type, jobs.StatusFailed).Inc()
	if err := h.auditor.Record(ctx, "system", deviceID, "JobFailed", map[string]any{
		"job_id": job.ID, "command_key": job.CommandKey, "type": job.Type,
		"fault_code": code, "fault_string": msg,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Warn("job failed", "job_id", job.ID, "command_key", job.CommandKey, "type", job.Type,
		"device_id", deviceID, "fault_code", code, "fault_string", msg)
}

func (h *handler) cacheParameterValues(ctx context.Context, deviceID string, list []cwmp.ParameterValueStruct, source string) {
	if len(list) == 0 {
		return
	}
	values := make(map[string]parameters.CachedValue, len(list))
	now := time.Now().UTC()
	for _, p := range list {
		values[p.Name] = parameters.CachedValue{Value: p.Value, UpdatedAt: now, Source: source}
	}
	if err := h.params.Upsert(ctx, deviceID, values); err != nil {
		h.logger.Error("failed to upsert parameter cache", "err", err, "device_id", deviceID, "source", source)
	}
}

// extractConnectionRequestURL pulls ManagementServer.ConnectionRequestURL
// out of an Inform's ParameterList, checking both data model roots (v3
// §6.1) since data_model_root detection isn't wired up yet — a device
// reporting under either root gets its Connection Request URL captured.
func extractConnectionRequestURL(list []cwmp.ParameterValueStruct) string {
	for _, p := range list {
		switch p.Name {
		case "Device.ManagementServer.ConnectionRequestURL",
			"InternetGatewayDevice.ManagementServer.ConnectionRequestURL":
			return p.Value
		}
	}
	return ""
}

// extractSTUNStatus pulls ManagementServer.UDPConnectionRequestAddress and
// .NATDetected out of an Inform's ParameterList (critical feature backlog:
// STUN NAT traversal) — checking both data model roots, same convention as
// extractConnectionRequestURL. Returns ok=false if the CPE didn't report
// UDPConnectionRequestAddress at all (most CPEs, since it's only populated
// once STUN is enabled and has actually bound), so callers don't overwrite
// a real previous value with an empty one just because one Inform happened
// not to carry it.
func extractSTUNStatus(list []cwmp.ParameterValueStruct) (addr string, natDetected bool, ok bool) {
	for _, p := range list {
		switch p.Name {
		case "Device.ManagementServer.UDPConnectionRequestAddress",
			"InternetGatewayDevice.ManagementServer.UDPConnectionRequestAddress":
			addr = p.Value
			ok = true
		case "Device.ManagementServer.NATDetected",
			"InternetGatewayDevice.ManagementServer.NATDetected":
			natDetected = p.Value == "1" || p.Value == "true"
		}
	}
	return addr, natDetected, ok
}

func (h *handler) closeSession(ctx context.Context, w http.ResponseWriter, sessionID, deviceID string) {
	if err := h.sessions.Close(ctx, sessionID, "NO_PENDING_RPCS"); err != nil {
		h.logger.Error("failed to close session", "err", err, "session_id", sessionID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.auditor.Record(ctx, "system", deviceID, "SessionClosed", map[string]any{
		"session_id": sessionID,
		"reason":     "NO_PENDING_RPCS",
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	h.logger.Info("session closed", "session_id", sessionID, "device_id", deviceID, "reason", "NO_PENDING_RPCS")
	h.respondEmpty(w)
}

func (h *handler) respondEmpty(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(http.StatusOK)
}
