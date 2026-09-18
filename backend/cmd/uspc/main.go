// Command uspc is the USP (TR-369) controller service (design §4.1,
// build plan's USP transport plan Task 5): it terminates the WebSocket
// and MQTT MTP bindings (internal/usp/mtp), decodes inbound Records into
// Msgs (internal/usp), probes every newly connected agent with a
// Get(["Device.DeviceInfo."]) -- interop evidence, not real dispatch --
// reconciles agent identity (OnBoardRequest, or the probe's own GetResp
// as a last resort) to a devices row via internal/devices and
// internal/store, and dispatches internal/jobs' queued work over USP
// (dispatch.go/dispatcher.go, usp-job-dispatch plan): a Postgres NOTIFY
// for an already-connected device, a fresh identity reconcile for a
// device that queued a job before it connected, and a periodic sweep as
// the safety net all converge on dispatcher.tryDispatch.
//
// Production admission has three independent layers: TLS client-certificate
// verification, a durable certificate-fingerprint -> device/EndpointID/topic
// principal binding, and a CIDR allowlist as defence in depth. Lab retains
// explicit compatibility behavior for reference-agent and hardware discovery
// work, including plaintext when ACS_USP_ALLOW_PLAINTEXT=true.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"acs/internal/bss"
	"acs/internal/captures"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/observability"
	"acs/internal/parameters"
	"acs/internal/store"
	"acs/internal/subscriptions"
	"acs/internal/usp/mtp"
	"acs/internal/usp/principal"
	"acs/internal/uspprincipal"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("refusing to start", "err", err)
		os.Exit(1)
	}
}

// run does everything main() would otherwise do inline, so main() stays
// a one-liner and every step here stays testable-by-inspection: load
// config, start the HTTP server (so /readyz can genuinely report 503
// while the transports are still coming up), wire and start the
// transports, then block until shutdown.
func run(logger *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv, logger)
	if err != nil {
		return err
	}
	// These are file paths/identifiers, not secret material. Logging them
	// makes the effective startup posture visible without exposing keys.
	logger.Info("config", "var", "ACS_USP_CONTROLLER_ID", "value", cfg.ControllerID)
	logger.Info("config", "var", "ACS_USP_TLS_CERT", "value", cfg.TLSCert)
	logger.Info("config", "var", "ACS_USP_TLS_KEY", "value", cfg.TLSKey)
	logger.Info("config", "var", "ACS_USP_CLIENT_CA_CERT", "value", cfg.TLSClientCA)
	if len(cfg.AllowedCIDRs) == 0 {
		logger.Warn("ACS_USP_ALLOWED_CIDRS is not set: any network can reach this service's listeners. Set it for a production deployment.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// store.Open verifies connectivity itself (a ping, right after
	// opening) so a bad DSN fails fast here rather than silently on this
	// service's first query.
	db, err := store.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}

	// A fresh process start means every previously-recorded USP
	// connection is definitely gone (design spec §6.1's single-instance
	// posture): nothing else clears usp_agents.connected on a crash or
	// restart, a resolveAndMarkReconciled failure after LinkUspAgent
	// already ran, or a takeover connection that never itself
	// reconciles. Left uncleared, any of those permanently exempts a
	// device from the liveness reaper's UNREACHABLE marking. Must run
	// before the transports start accepting reconnects.
	if err := resetUspAgentsConnected(ctx, db, logger); err != nil {
		db.Close()
		return err
	}

	metrics := observability.NewMetrics("uspc")
	uspm := newUSPMetrics(metrics)

	tlsConfig, err := loadTLSConfig(cfg.TLSCert, cfg.TLSKey, cfg.TLSClientCA)
	if err != nil {
		db.Close()
		return fmt.Errorf("load TLS configuration: %w", err)
	}

	var ready atomic.Bool
	server, serverErrCh := startHTTPServer(cfg.HTTPAddr, metrics, &ready)
	logger.Info("uspc http server listening", "addr", cfg.HTTPAddr)

	p := newProbe(cfg.ControllerID, logger)
	p.setMetrics(uspm)
	repo := devices.NewRepository(db)
	principalRepo := uspprincipal.NewRepository(db)
	registry := mtp.NewRegistry()
	jobsRepo := jobs.NewRepository(db)
	capturesRepo := captures.NewRepository(db)
	disp := newDispatcher(jobsRepo, repo, registry, cfg.ControllerID, logger)
	disp.captures = capturesRepo
	disp.devices = repo
	paramsRepo := parameters.NewRepository(db)
	disp.paramsRepo = paramsRepo
	subsRepo := subscriptions.NewRepository(db)
	subsReconciler := newSubscriptionReconciler(subsRepo, cfg.ControllerID, logger)
	h := &handler{
		log:           logger,
		registry:      registry,
		probe:         p,
		controllerID:  cfg.ControllerID,
		metrics:       uspm,
		reconciler:    newReconciler(repo, logger),
		dispatcher:    disp,
		subscriptions: subsReconciler,
		paramsRepo:    paramsRepo,
		devicesRepo:   repo,
		captures:      capturesRepo,
		tmfEvents:     bss.NewRepository(db),
	}

	// dispatchCtx bounds the two dispatch goroutines below independently of
	// ctx's own cancellation timing. shutdown explicitly cancels it on both
	// signal and HTTP-server-error paths before waiting on dispatchWG.
	dispatchCtx, dispatchCancel := context.WithCancel(ctx)
	defer dispatchCancel()
	var dispatchWG sync.WaitGroup

	var principalAuth principal.CertificateAuthenticator
	if cfg.DeploymentProfile == deploymentProfileProduction {
		principalAuth = principalRepo
		h.reconciler.usePrincipalStore(principalRepo)
	}
	ws, mq, err := newTransports(cfg, tlsConfig, principalAuth, logger)
	if err != nil {
		shutdown(logger, server, nil, nil, nil, db, dispatchCancel, &dispatchWG)
		return err
	}

	if err := startTransports(ctx, ws, mq, h); err != nil {
		shutdown(logger, server, ws, mq, nil, db, dispatchCancel, &dispatchWG)
		return err
	}

	// jobs.Listen and the two goroutines below are the push (NOTIFY) and
	// safety-net (periodic sweep) dispatch triggers (design S6.1); the
	// third trigger, a fresh identity reconcile, runs inline from
	// handler.resolveAndMarkReconciled and needs no wiring here.
	listener, err := jobs.Listen(ctx, db, jobs.NotifyChannel, logger)
	if err != nil {
		shutdown(logger, server, ws, mq, nil, db, dispatchCancel, &dispatchWG)
		return fmt.Errorf("start job queue listener: %w", err)
	}
	dispatchWG.Add(2)
	go func() {
		defer dispatchWG.Done()
		drainDispatchNotifications(dispatchCtx, listener, disp, logger)
	}()
	go func() {
		defer dispatchWG.Done()
		disp.periodicSweep(dispatchCtx, registry, dispatchSweepInterval)
	}()

	ready.Store(true)
	logger.Info("uspc listening", "controller_id", cfg.ControllerID,
		"ws_addr", ws.Addr(), "mqtt_addr", mq.Addr(), "http_addr", cfg.HTTPAddr,
		"tls", tlsConfig != nil, "principal_auth", principalAuth != nil)

	return waitForShutdown(ctx, logger, server, serverErrCh, ws, mq, listener, db, dispatchCancel, &dispatchWG)
}

// drainDispatchNotifications forwards every device id delivered on
// listener's Notifications() channel into disp.tryDispatch -- the NOTIFY
// trigger path for a device that is already connected when its job is
// queued (design S6.1).
func drainDispatchNotifications(ctx context.Context, listener *jobs.QueueListener, disp *dispatcher, logger *slog.Logger) {
	for deviceID := range listener.Notifications() {
		if err := disp.tryDispatch(ctx, deviceID); err != nil {
			logger.Warn("uspc: dispatcher: failed to dispatch after job-queued notification", "device_id", deviceID, "error", err)
		}
	}
}

// resetUspAgentsConnected clears connected=true on every usp_agents row.
// Called once at startup, before the transports begin accepting
// connections -- a one-shot correction for state that can only be stale
// at this point in the process's life.
func resetUspAgentsConnected(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	result, err := db.ExecContext(ctx, `UPDATE usp_agents SET connected = false WHERE connected`)
	if err != nil {
		return fmt.Errorf("reset usp_agents.connected at startup: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		logger.Warn("uspc: reset usp_agents.connected at startup but could not determine how many rows were affected", "error", err)
		return nil
	}
	logger.Info("uspc: reset stale usp_agents.connected rows at startup", "rows_reset", n)
	return nil
}

// loadTLSConfig builds the shared *tls.Config both transports serve over,
// or nil for plaintext. When clientCAFile is set, every network peer must
// present a certificate chaining to that CA before MQTT/WebSocket code can
// perform the application-level fingerprint lookup.
func loadTLSConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS keypair: %w", err)
	}

	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	if clientCAFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read USP client CA certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("USP client CA certificate did not contain a valid PEM certificate")
	}
	cfg.MinVersion = tls.VersionTLS12
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
	cfg.ClientCAs = pool
	return cfg, nil
}

// newTransports constructs the WebSocket and MQTT transports from cfg,
// without starting either -- Start is a separate step so run can log
// once both are known to be valid.
func newTransports(cfg serviceConfig, tlsConfig *tls.Config, principalAuth principal.CertificateAuthenticator, logger *slog.Logger) (*mtp.WebSocket, *mtp.MQTT, error) {
	ws, err := mtp.NewWebSocket(mtp.WebSocketConfig{
		Addr:                   cfg.WSAddr,
		Path:                   cfg.WSPath,
		ControllerEndpointID:   cfg.ControllerID,
		TLS:                    tlsConfig,
		AllowPlaintext:         cfg.AllowPlaintext,
		AllowedCIDRs:           cfg.AllowedCIDRs,
		PrincipalAuthenticator: principalAuth,
	}, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("construct WebSocket transport: %w", err)
	}

	mq, err := mtp.NewMQTT(mtp.MQTTConfig{
		Addr:                   cfg.MQTTAddr,
		ControllerTopic:        cfg.MQTTControllerTopic,
		ControllerEndpointID:   cfg.ControllerID,
		TLS:                    tlsConfig,
		AllowPlaintext:         cfg.AllowPlaintext,
		AllowedCIDRs:           cfg.AllowedCIDRs,
		PrincipalAuthenticator: principalAuth,
	}, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("construct MQTT transport: %w", err)
	}
	return ws, mq, nil
}

// startTransports starts both transports against h. Each Start binds
// its listener synchronously before returning, so by the time this
// returns nil both are actually accepting connections -- the condition
// /readyz reports.
func startTransports(ctx context.Context, ws *mtp.WebSocket, mq *mtp.MQTT, h *handler) error {
	if err := ws.Start(ctx, h); err != nil {
		return fmt.Errorf("start WebSocket transport: %w", err)
	}
	if err := mq.Start(ctx, h); err != nil {
		return fmt.Errorf("start MQTT transport: %w", err)
	}
	return nil
}

// startHTTPServer mounts /healthz, /readyz, /metrics and starts serving
// in the background, returning immediately -- so run can start it before
// the transports exist, and /readyz genuinely answers 503 while they are
// still coming up.
func startHTTPServer(addr string, metrics *observability.Metrics, ready *atomic.Bool) (*http.Server, <-chan error) {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("GET /healthz", observability.LivenessHandler())
	mux.Handle("GET /readyz", readinessHandler(ready))

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	return server, errCh
}

// waitForShutdown blocks until ctx is canceled or the HTTP server itself
// fails, then drains everything.
func waitForShutdown(ctx context.Context, logger *slog.Logger, server *http.Server, serverErrCh <-chan error, ws *mtp.WebSocket, mq *mtp.MQTT, listener *jobs.QueueListener, db *sql.DB, dispatchCancel context.CancelFunc, dispatchWG *sync.WaitGroup) error {
	select {
	case err := <-serverErrCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			shutdown(logger, server, ws, mq, listener, db, dispatchCancel, dispatchWG)
			return fmt.Errorf("http server error: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutting down")
	}
	shutdown(logger, server, ws, mq, listener, db, dispatchCancel, dispatchWG)
	return nil
}

// shutdown drains server and, when non-nil, both transports and the job
// queue listener, waits (bounded by the same shutdownCtx timeout the rest
// of this function already uses) for the two dispatch goroutines tracked
// on dispatchWG to finish, then closes db.
func shutdown(logger *slog.Logger, server *http.Server, ws *mtp.WebSocket, mq *mtp.MQTT, listener *jobs.QueueListener, db *sql.DB, dispatchCancel context.CancelFunc, dispatchWG *sync.WaitGroup) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown did not complete cleanly", "err", err)
	}
	if ws != nil {
		if err := ws.Stop(shutdownCtx); err != nil {
			logger.Warn("WebSocket shutdown did not complete cleanly", "err", err)
		}
	}
	if mq != nil {
		if err := mq.Stop(shutdownCtx); err != nil {
			logger.Warn("MQTT shutdown did not complete cleanly", "err", err)
		}
	}
	if listener != nil {
		if err := listener.Close(); err != nil {
			logger.Warn("job queue listener shutdown did not complete cleanly", "err", err)
		}
	}
	if dispatchCancel != nil {
		dispatchCancel()
	}
	if dispatchWG != nil {
		dispatchDone := make(chan struct{})
		go func() {
			dispatchWG.Wait()
			close(dispatchDone)
		}()
		select {
		case <-dispatchDone:
		case <-shutdownCtx.Done():
			logger.Warn("dispatch goroutines (NOTIFY drain / periodic sweep) did not stop within the shutdown timeout")
		}
	}
	if db != nil {
		if err := db.Close(); err != nil {
			logger.Warn("postgres shutdown did not complete cleanly", "err", err)
		}
	}
}

// readinessHandler reports 200 once ready is true -- both transports
// have started -- and 503 before that. Postgres connectivity is already
// established at startup by store.Open.
func readinessHandler(ready *atomic.Bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
}
