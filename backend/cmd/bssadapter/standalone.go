package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// standaloneHandler is the whole adapter when ACS_INTEGRATION_MODE is
// standalone: it reports healthy, so launchers and health checks that
// expect the process need no special case, and it refuses every
// integration route. There is no database connection, no credential and
// no background worker: nothing is delivered, reconciled or dispatched.
func standaloneHandler() http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","integration_mode":"standalone"}` + "\n"))
	}
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusServiceUnavailable, "ErrIntegrationDisabled",
			"the BSS/NaaS integration is switched off on this ACS (ACS_INTEGRATION_MODE=standalone)")
	})
	return withProblemMediaType(mux)
}

// runStandalone serves standaloneHandler until the process is told to stop.
func runStandalone(logger *slog.Logger) {
	addr := envOr("ACS_BSS_ADDR", ":8090")
	logger.Warn("ACS_INTEGRATION_MODE=standalone: the BSS/NaaS integration is switched off. /bss/v1 and /tmf-api answer 503 and no integration worker runs. Set ACS_INTEGRATION_MODE=naas to switch it on.", "addr", addr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: addr, Handler: standaloneHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
}
