// Command uspc is the USP (TR-369) controller service (design §4.1,
// build plan's USP transport plan Task 5): it terminates the WebSocket
// and MQTT MTP bindings (internal/usp/mtp), decodes inbound Records into
// Msgs (internal/usp), probes every newly connected agent with a
// Get(["Device.DeviceInfo."]) -- interop evidence, not real dispatch --
// and reconciles agent identity (OnBoardRequest, or the probe's own
// GetResp as a last resort) to a devices row via internal/devices and
// internal/store.
//
// It is otherwise wiring only. Job dispatch is a later plan; this
// service still deliberately never imports internal/jobs
// (boundary_test.go enforces this) -- cmd/uspc is where the USP protocol
// core meets transport and identity, not where it meets job dispatch.
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
	"sync/atomic"
	"syscall"
	"time"

	"acs/internal/devices"
	"acs/internal/observability"
	"acs/internal/store"
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
	h := &handler{
		log:          logger,
		registry:     mtp.NewRegistry(),
		probe:        p,
		controllerID: cfg.ControllerID,
		metrics:      uspm,
		reconciler:   newReconciler(repo, logger),
	}

	ws, mq, err := newTransports(cfg, tlsConfig, logger)
	if err != nil {
		shutdown(logger, server, nil, nil, db)
		return err
	}

	if err := startTransports(ctx, ws, mq, h); err != nil {
		shutdown(logger, server, ws, mq, db)
		return err
	}
	ready.Store(true)
	logger.Info("uspc listening", "controller_id", cfg.ControllerID,
		"ws_addr", ws.Addr(), "mqtt_addr", mq.Addr(), "http_addr", cfg.HTTPAddr, "tls", tlsConfig != nil)

	return waitForShutdown(ctx, logger, server, serverErrCh, ws, mq, db)
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
func waitForShutdown(ctx context.Context, logger *slog.Logger, server *http.Server, serverErrCh <-chan error, ws *mtp.WebSocket, mq *mtp.MQTT, db *sql.DB) error {
	select {
	case err := <-serverErrCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			shutdown(logger, server, ws, mq, db)
			return fmt.Errorf("http server error: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutting down")
	}
	shutdown(logger, server, ws, mq, db)
	return nil
}

// shutdown drains server and, when non-nil, both transports, then closes
// db, logging (rather than failing the caller) on any error. ws and mq
// are nil when called from an early-return path where the HTTP server
// was already started but the transports never got as far as existing.
// db is closed last, after both transports and the HTTP server have
// stopped, so in-flight identity reconciliation work is not cut off
// mid-shutdown.
func shutdown(logger *slog.Logger, server *http.Server, ws *mtp.WebSocket, mq *mtp.MQTT, db *sql.DB) {
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
