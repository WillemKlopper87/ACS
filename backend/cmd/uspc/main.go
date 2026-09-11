// Command uspc is the USP (TR-369) controller service (design §4.1,
// build plan's USP transport plan Task 5): it terminates the WebSocket
// and MQTT MTP bindings (internal/usp/mtp), decodes inbound Records into
// Msgs (internal/usp), and probes every newly connected agent with a
// Get(["Device.DeviceInfo."]) -- interop evidence, not real dispatch.
//
// It is wiring only. Real device-model reconciliation, job dispatch, and
// persistence are a later plan (B-3); this service deliberately imports
// nothing from internal/devices, internal/jobs, or internal/store
// (boundary_test.go enforces this) -- cmd/uspc is where the USP protocol
// core meets transport, not where it meets the domain.
//
// Agent allowlisting does not exist yet: any agent that completes the
// WebSocket subprotocol/query-parameter handshake or publishes to the
// MQTT controller topic is accepted. Do not expose this service to an
// untrusted network until that lands.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"acs/internal/config"
	"acs/internal/observability"
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
// config, wire the transports and HTTP server, block until shutdown.
func run(logger *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv, logger)
	if err != nil {
		return err
	}
	config.LogSummary(logger,
		config.Secret{Env: "ACS_USP_CONTROLLER_ID"},
		config.Secret{Env: "ACS_USP_TLS_CERT"},
		config.Secret{Env: "ACS_USP_TLS_KEY"},
	)
	logger.Warn("agent allowlisting is not implemented in this build; do not expose uspc to an untrusted network")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	metrics := observability.NewMetrics("uspc")
	uspm := newUSPMetrics(metrics)

	tlsConfig, err := loadTLSConfig(cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		return fmt.Errorf("load TLS configuration: %w", err)
	}

	h := &handler{
		log:          logger,
		registry:     mtp.NewRegistry(),
		probe:        newProbe(cfg.ControllerID, logger),
		controllerID: cfg.ControllerID,
		metrics:      uspm,
	}

	ws, mq, err := newTransports(cfg, tlsConfig, logger)
	if err != nil {
		return err
	}

	var ready atomic.Bool
	if err := startTransports(ctx, ws, mq, h); err != nil {
		return err
	}
	ready.Store(true)
	logger.Info("uspc listening", "controller_id", cfg.ControllerID,
		"ws_addr", ws.Addr(), "mqtt_addr", mq.Addr(), "http_addr", cfg.HTTPAddr, "tls", tlsConfig != nil)

	return serveHTTP(ctx, logger, cfg.HTTPAddr, metrics, &ready, ws, mq)
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

// serveHTTP runs the /healthz, /readyz, /metrics server until ctx is
// canceled or the server itself fails, then drains it and both
// transports.
func serveHTTP(ctx context.Context, logger *slog.Logger, addr string, metrics *observability.Metrics, ready *atomic.Bool, ws *mtp.WebSocket, mq *mtp.MQTT) error {
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

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server error: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown did not complete cleanly", "err", err)
	}
	if err := ws.Stop(shutdownCtx); err != nil {
		logger.Warn("WebSocket shutdown did not complete cleanly", "err", err)
	}
	if err := mq.Stop(shutdownCtx); err != nil {
		logger.Warn("MQTT shutdown did not complete cleanly", "err", err)
	}
	return nil
}

// readinessHandler reports 200 once ready is true -- both transports
// have started -- and 503 before that. There is no database in this
// plan, so unlike internal/observability.ReadinessHandler this never
// depends on *sql.DB.
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
