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
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"acs/internal/auth"
	"acs/internal/config"
	"acs/internal/credentials"
	"acs/internal/devices"
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

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelFromEnv()}))

	dsn := os.Getenv("ACS_POSTGRES_DSN")
	if dsn == "" {
		logger.Error("ACS_POSTGRES_DSN is required (e.g. postgres://acs:acs@localhost:5432/acs?sslmode=disable)")
		os.Exit(1)
	}

	// Fail-closed CPE authentication (audit P0.1): the CWMP listener must
	// authenticate devices via Digest credentials or mTLS; the historical
	// unauthenticated mode now requires ACS_INSECURE_DEV_MODE=true.
	cpeAuthSecrets := []config.Secret{
		{Env: "ACS_DIGEST_PASSWORD", MinBytes: 16, Purpose: "authenticates CPE CWMP sessions via HTTP Digest"},
		{Env: "ACS_MTLS_CA_CERT", MinBytes: 1, Purpose: "authenticates CPE CWMP sessions via client certificates"},
	}
	// Per-device Digest credentials are read from device_credentials,
	// which cmd/api encrypts with this key; without it, rows written by
	// an encrypting cmd/api cannot be decrypted here.
	if err := config.Validate(logger, config.Secret{Env: "ACS_CREDENTIAL_ENCRYPTION_KEY", MinBytes: 16, Purpose: "decrypts per-device CWMP Digest credentials", Optional: true}); err != nil {
		logger.Error("refusing to start", "err", err)
		os.Exit(1)
	}
	if err := config.RequireOneOf(logger, "the CWMP endpoint would otherwise accept unauthenticated devices", cpeAuthSecrets...); err != nil {
		logger.Error("refusing to start", "err", err)
		os.Exit(1)
	}
	config.LogSummary(logger, cpeAuthSecrets...)

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

	credRepo, err := credentials.NewRepository(db, os.Getenv("ACS_CREDENTIAL_ENCRYPTION_KEY"))
	if err != nil {
		logger.Error("failed to initialize credential decryption", "err", err)
		os.Exit(1)
	}
	if os.Getenv("ACS_CREDENTIAL_ENCRYPTION_KEY") == "" {
		logger.Warn("ACS_CREDENTIAL_ENCRYPTION_KEY not set on cmd/acs — per-device CWMP Digest credentials encrypted by cmd/api will not verify here")
	}
	authr := auth.DigestAuthenticator{
		Username:   os.Getenv("ACS_DIGEST_USERNAME"),
		Password:   os.Getenv("ACS_DIGEST_PASSWORD"),
		AllowBasic: envBool("ACS_AUTH_ALLOW_BASIC"),
		// Per-device credentials (audit P0.5): a username that isn't the
		// shared one is looked up in device_credentials; the first
		// successful use of a PENDING credential activates it.
		Lookup: func(username string) (string, bool) {
			cred, err := credRepo.LookupCWMPDigest(context.Background(), username)
			if err != nil {
				return "", false
			}
			return cred.Password, true
		},
		OnAuthenticated: func(username string) {
			ctx := context.Background()
			cred, err := credRepo.LookupCWMPDigest(ctx, username)
			if err != nil || cred.Status != credentials.StatusPending {
				return
			}
			if _, err := credRepo.Activate(ctx, cred.ID); err != nil {
				logger.Error("failed to auto-activate per-device digest credential", "err", err, "credential_id", cred.ID)
				return
			}
			logger.Info("per-device CWMP Digest credential activated on first use", "device_id", cred.DeviceID, "version", cred.Version)
			_ = observability.NewAuditor(db).Record(ctx, "system", cred.DeviceID, "CredentialRotation", map[string]any{
				"credential_id": cred.ID, "version": cred.Version, "type": credentials.TypeCWMPDigest, "phase": "activated-on-first-use",
			})
		},
	}
	if authr.Password == "" {
		// Per-device-only (or mTLS) fleets: nonces still need a key.
		authr.NonceSecret = []byte(os.Getenv("ACS_CREDENTIAL_ENCRYPTION_KEY"))
	}
	if !authr.Enabled() {
		logger.Warn("ACS_DIGEST_USERNAME/ACS_DIGEST_PASSWORD not set — CWMP endpoint is running WITHOUT authentication (design doc v3 §11.1/§11.2).")
	}
	if authr.AllowBasic {
		logger.Info("HTTP Basic fallback enabled for CWMP auth (ACS_AUTH_ALLOW_BASIC) — use only with TLS in production")
	}

	metrics := observability.NewMetrics("acs")
	metrics.ObserveDB(db)

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
	go runLeaseReaper(ctx, h.jobs, metrics, logger)

	mux := http.NewServeMux()
	// Catch-all rather than exact "/cwmp": CPEs in the field get provisioned
	// with ACS URLs like "http://host:7547/", "/cwmp/", or "/acs", and a 404
	// on the path mismatch looks to the operator exactly like "device won't
	// connect". Any POST that reaches this server is treated as CWMP;
	// handleCWMP logs the path so misconfigured device URLs stay visible.
	mux.HandleFunc("/", h.handleCWMP)
	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("GET /healthz", observability.LivenessHandler())
	mux.Handle("GET /readyz", observability.ReadinessHandler(db))

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
