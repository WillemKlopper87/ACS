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
// Agent allowlisting does not exist yet: any agent that completes the
// WebSocket subprotocol/query-parameter handshake or publishes to the
// MQTT controller topic is accepted. Do not expose this service to an
// untrusted network until that lands.
package main

import (
	"context"
	"crypto/tls"
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

	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/observability"
	"acs/internal/parameters"
	"acs/internal/store"
	"acs/internal/subscriptions"
	"acs/internal/usp/mtp"
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
	// None of these three are secrets: ACS_USP_TLS_CERT/ACS_USP_TLS_KEY
	// are file paths, not key material, and ACS_USP_CONTROLLER_ID is
	// deliberately published to every connected agent (see loadConfig's
	// doc comment) -- config.LogSummary's redact-to-"set (N bytes)"
	// treatment would only hide information useful in a startup summary,
	// so these are logged plainly instead of passed through it.
	logger.Info("config", "var", "ACS_USP_CONTROLLER_ID", "value", cfg.ControllerID)
	logger.Info("config", "var", "ACS_USP_TLS_CERT", "value", cfg.TLSCert)
	logger.Info("config", "var", "ACS_USP_TLS_KEY", "value", cfg.TLSKey)
	logger.Warn("agent allowlisting is not implemented in this build; do not expose uspc to an untrusted network")

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
	// device from the liveness reaper's UNREACHABLE marking -- the exact
	// regression this plan's Task 4 was built to fix (final-review
	// finding 2). Must run before the transports start accepting
	// connections, so a real reconnect's own LinkUspAgent can never race
	// this reset and have its fresh connected=true clobbered back to
	// false.
	if err := resetUspAgentsConnected(ctx, db, logger); err != nil {
		db.Close()
		return err
	}

	metrics := observability.NewMetrics("uspc")
	uspm := newUSPMetrics(metrics)

	tlsConfig, err := loadTLSConfig(cfg.TLSCert, cfg.TLSKey)
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
	registry := mtp.NewRegistry()
	jobsRepo := jobs.NewRepository(db)
	disp := newDispatcher(jobsRepo, repo, registry, cfg.ControllerID, logger)
	paramsRepo := parameters.NewRepository(db)
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
	}

	// dispatchCtx bounds the two dispatch goroutines below independently of
	// ctx's own cancellation timing (fix round 1, Important 4): waitForShutdown's
	// http-server-error branch calls shutdown without ever canceling ctx
	// itself (ctx is only canceled by run's own deferred stop(), after
	// shutdown has already returned), so a goroutine that only stopped on
	// ctx.Done() would never receive a stop signal on that path and
	// dispatchWG.Wait() below would block for the full shutdown timeout for
	// nothing. dispatchCancel is called explicitly inside shutdown instead,
	// so both shutdown paths behave the same way.
	dispatchCtx, dispatchCancel := context.WithCancel(ctx)
	defer dispatchCancel()
	var dispatchWG sync.WaitGroup

	ws, mq, err := newTransports(cfg, tlsConfig, logger)
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
	// drainDispatchNotifications's own loop actually ends when
	// listener.Notifications() closes (which shutdown's explicit
	// listener.Close() call causes); periodicSweep's loop ends on
	// dispatchCtx.Done(). Both are tracked on dispatchWG so shutdown can
	// wait for them to actually finish, not just signal them to stop (see
	// shutdown's own doc comment).
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
		"ws_addr", ws.Addr(), "mqtt_addr", mq.Addr(), "http_addr", cfg.HTTPAddr, "tls", tlsConfig != nil)

	return waitForShutdown(ctx, logger, server, serverErrCh, ws, mq, listener, db, dispatchCancel, &dispatchWG)
}

// drainDispatchNotifications forwards every device id delivered on
// listener's Notifications() channel into disp.tryDispatch -- the NOTIFY
// trigger path for a device that is already connected when its job is
// queued (design S6.1). The loop ends when Notifications() closes, which
// happens once listener's own background goroutine exits (ctx canceled,
// or listener.Close called) -- no separate stop signal is needed here.
func drainDispatchNotifications(ctx context.Context, listener *jobs.QueueListener, disp *dispatcher, logger *slog.Logger) {
	for deviceID := range listener.Notifications() {
		if err := disp.tryDispatch(ctx, deviceID); err != nil {
			logger.Warn("uspc: dispatcher: failed to dispatch after job-queued notification", "device_id", deviceID, "error", err)
		}
	}
}

// resetUspAgentsConnected clears connected=true on every usp_agents row.
// Called once at startup, before the transports begin accepting
// connections (see run's call site for the full rationale) -- a one-shot
// correction for state that can only be stale at this point in the
// process's life, not an ongoing repository method, so a plain
// ExecContext here is enough; it doesn't need a *devices.Repository
// method of its own.
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

// loadTLSConfig builds the shared *tls.Config both transports serve
// over, or nil for plaintext (cfg has already validated that plaintext
// is opted into when no cert/key pair is set).
func loadTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS keypair: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
}

// newTransports constructs the WebSocket and MQTT transports from cfg,
// without starting either -- Start is a separate step so run can log
// once both are known to be valid.
func newTransports(cfg serviceConfig, tlsConfig *tls.Config, logger *slog.Logger) (*mtp.WebSocket, *mtp.MQTT, error) {
	ws, err := mtp.NewWebSocket(mtp.WebSocketConfig{
		Addr:           cfg.WSAddr,
		Path:           cfg.WSPath,
		TLS:            tlsConfig,
		AllowPlaintext: cfg.AllowPlaintext,
		AllowedCIDRs:   cfg.AllowedCIDRs,
	}, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("construct WebSocket transport: %w", err)
	}

	mq, err := mtp.NewMQTT(mtp.MQTTConfig{
		Addr:                 cfg.MQTTAddr,
		ControllerTopic:      cfg.MQTTControllerTopic,
		ControllerEndpointID: cfg.ControllerID,
		TLS:                  tlsConfig,
		AllowPlaintext:       cfg.AllowPlaintext,
		AllowedCIDRs:         cfg.AllowedCIDRs,
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
// the transports exist, and /readyz genuinely answers 503 (ready is
// still false) for the window between the HTTP server coming up and the
// transports finishing Start, rather than only ever being reachable
// once both are already true.
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
// of this function already uses) for the two dispatch goroutines
// (drainDispatchNotifications, periodicSweep) tracked on dispatchWG to
// actually finish -- not merely signaled to stop via dispatchCancel --
// then closes db, logging (rather than failing the caller) on any error.
// ws, mq and listener are nil when called from an early-return path where
// the HTTP server was already started but the piece in question never
// got as far as existing; dispatchCancel/dispatchWG are always non-nil
// (constructed before the first shutdown call site in run), guarding them
// anyway costs nothing and keeps this function safe to call from a future
// early-return path that predates their construction. db is closed last,
// after the transports, the job queue listener, and the dispatch
// goroutines have all stopped, so in-flight identity reconciliation and
// dispatch work is not cut off mid-shutdown (fix round 1, Important 4 --
// this used to only be true for identity reconciliation, since nothing
// waited for the dispatch goroutines to actually exit before db.Close()
// ran), and listener's dedicated connection is released back to db's pool
// before db itself closes.
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
// have started -- and 503 before that. It does not itself depend on
// *sql.DB the way internal/observability.ReadinessHandler does:
// connectivity to Postgres is established once at startup (run calls
// store.Open, which pings) and failure there refuses to start the
// process at all, rather than being polled here on every readiness
// check.
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
