// Command api is the REST API (build plan §4). Phase 1 added the
// read-only device endpoints. Phase 2 added the first write endpoint —
// PUT parameters queues a SET_PARAMETER job and returns 202 Accepted
// (design doc v3 §8, "all write endpoints ... return 202") — plus reading
// the parameter cache and job status. Phase 3 adds Connection Request:
// unlike SET_PARAMETER/GET_PARAMETER, that job type isn't triggered by an
// inbound CWMP request, so this process also runs a small background
// worker (connreq_worker.go) that goes looking for queued
// CONNECTION_REQUEST jobs instead of waiting for one to be dispatched to
// it.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"acs/internal/bss"
	"acs/internal/cliaccess"
	"acs/internal/config"
	"acs/internal/credentials"
	"acs/internal/dashboard"
	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/firmware"
	"acs/internal/jobs"
	"acs/internal/mailer"
	"acs/internal/netguard"
	"acs/internal/objstore"
	"acs/internal/observability"
	"acs/internal/operators"
	"acs/internal/parameters"
	"acs/internal/policy"
	"acs/internal/ratelimit"
	"acs/internal/rollout"
	"acs/internal/scheduler"
	"acs/internal/store"
	"acs/internal/templates"
	"acs/internal/tenancy"
	"acs/internal/transfer"
	"acs/internal/uploads"
	"acs/internal/vpn"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	dsn := os.Getenv("ACS_POSTGRES_DSN")
	if dsn == "" {
		logger.Error("ACS_POSTGRES_DSN is required")
		os.Exit(1)
	}

	// Fail-closed secret enforcement (audit P0.1): without these, the
	// API used to run wide open with only a warning. That mode now
	// requires the explicit ACS_INSECURE_DEV_MODE=true escape hatch.
	apiSecrets := []config.Secret{
		{Env: "ACS_JWT_SIGNING_SECRET", MinBytes: 32, Purpose: "signs operator JWTs; without it every request is anonymous"},
		{Env: "ACS_CREDENTIAL_ENCRYPTION_KEY", MinBytes: 16, Purpose: "encrypts device/CLI/VPN credentials at rest; without it they are stored in plaintext"},
		{Env: "ACS_INTERNAL_SERVICE_TOKEN", MinBytes: 32, Purpose: "authenticates cmd/bssadapter's machine-to-machine calls into this API"},
		{Env: "ACS_BOOTSTRAP_ADMIN_PASSWORD", MinBytes: 12, Purpose: "seeds the first superadmin account", Optional: true},
		{Env: "ACS_BSS_API_TOKEN", MinBytes: 16, Purpose: "authenticates this API's troubleshooting calls to cmd/bssadapter", Optional: true},
	}
	if err := config.Validate(logger, apiSecrets...); err != nil {
		logger.Error("refusing to start", "err", err)
		os.Exit(1)
	}
	config.LogSummary(logger, apiSecrets...)

	// Process lifecycle (audit P1.2): SIGINT/SIGTERM cancels this context,
	// which stops both background workers, then the HTTP server drains.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, dsn)
	if err != nil {
		logger.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	firmwareRoot := envOr("ACS_FIRMWARE_STORAGE_ROOT", "./firmware-storage")
	firmwareStorage, err := objstore.FromEnv(logger, firmwareRoot, "firmware/")
	if err != nil {
		logger.Error("failed to initialize firmware storage", "err", err, "root", firmwareRoot)
		os.Exit(1)
	}

	uploadRoot := envOr("ACS_UPLOAD_STORAGE_ROOT", "./upload-storage")
	uploadStorage, err := objstore.FromEnv(logger, uploadRoot, "uploads/")
	if err != nil {
		logger.Error("failed to initialize upload storage", "err", err, "root", uploadRoot)
		os.Exit(1)
	}

	jwtSecret := []byte(os.Getenv("ACS_JWT_SIGNING_SECRET"))
	if len(jwtSecret) == 0 {
		logger.Warn("ACS_JWT_SIGNING_SECRET not set — cmd/api is running WITHOUT operator authentication (design doc v3 §11.3/§11.4). Every request is treated as an anonymous \"operator\".")
	}

	internalServiceToken := os.Getenv("ACS_INTERNAL_SERVICE_TOKEN")
	if len(jwtSecret) > 0 && internalServiceToken == "" {
		logger.Warn("ACS_INTERNAL_SERVICE_TOKEN not set — cmd/bssadapter's own calls into this API (order dispatch, job status) will get 401'd once JWT auth is enabled, since it has no operator login. Set the same value here and on cmd/bssadapter.")
	}

	credentialsRepo, err := credentials.NewRepository(db, os.Getenv("ACS_CREDENTIAL_ENCRYPTION_KEY"))
	if err != nil {
		logger.Error("failed to initialize credential encryption", "err", err)
		os.Exit(1)
	}
	if !credentialsRepo.Encrypted() {
		logger.Warn("ACS_CREDENTIAL_ENCRYPTION_KEY not set — device_credentials.password is stored in Postgres in plaintext (critical feature backlog item). Set it to any passphrase to encrypt at rest going forward; existing plaintext rows keep working unmigrated.")
	}

	cliRepo, err := cliaccess.NewRepository(db, os.Getenv("ACS_CREDENTIAL_ENCRYPTION_KEY"))
	if err != nil {
		logger.Error("failed to initialize cli credential encryption", "err", err)
		os.Exit(1)
	}

	overlaySubnetCIDR := envOr("ACS_VPN_OVERLAY_SUBNET", "10.99.0.0/16")
	_, overlaySubnet, err := net.ParseCIDR(overlaySubnetCIDR)
	if err != nil {
		logger.Error("invalid ACS_VPN_OVERLAY_SUBNET", "err", err, "value", overlaySubnetCIDR)
		os.Exit(1)
	}
	vpnRepo, err := vpn.NewRepository(db, os.Getenv("ACS_CREDENTIAL_ENCRYPTION_KEY"), overlaySubnet)
	if err != nil {
		logger.Error("failed to initialize vpn peer encryption", "err", err)
		os.Exit(1)
	}
	vpnConcentrator := vpn.ConcentratorConfig{
		ServerPublicKey: os.Getenv("ACS_VPN_SERVER_PUBLIC_KEY"),
		Endpoint:        os.Getenv("ACS_VPN_SERVER_ENDPOINT"),
		OverlaySubnet:   overlaySubnetCIDR,
	}
	if !vpnConcentrator.Configured() {
		logger.Warn("ACS_VPN_SERVER_PUBLIC_KEY/ACS_VPN_SERVER_ENDPOINT not set — VPN peers can still be enrolled (keypair + overlay IP allocation both work), but the generated client config will have an empty [Peer] section until a real concentrator host's public key/endpoint are configured. Deliberately last item in the admin-platform backlog — see internal/vpn's doc comment for the full scope.")
	}

	metrics := observability.NewMetrics("api")
	metrics.ObserveDB(db)

	var transferKey []byte
	if len(jwtSecret) > 0 {
		transferKey = transfer.DeriveKey(jwtSecret)
	}
	uploadMaxBytes := int64(256 << 20) // 256 MiB default ceiling per CPE upload
	if v := os.Getenv("ACS_UPLOAD_MAX_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			logger.Error("invalid ACS_UPLOAD_MAX_BYTES", "value", v)
			os.Exit(1)
		}
		uploadMaxBytes = n
	}

	// Outbound device-network policy for the web-GUI proxy and console
	// bridge (audit P0.4). Loopback/link-local/metadata/multicast targets
	// are always refused; setting ACS_DEVICE_NET_ALLOWED_CIDRS (comma-
	// separated, e.g. the VPN overlay plus the CPE management subnets)
	// additionally restricts targets to those networks — recommended for
	// any production deployment.
	netPolicy := netguard.Policy{}
	if v := os.Getenv("ACS_DEVICE_NET_ALLOWED_CIDRS"); v != "" {
		cidrs, err := netguard.ParseCIDRList(v)
		if err != nil {
			logger.Error("invalid ACS_DEVICE_NET_ALLOWED_CIDRS", "err", err)
			os.Exit(1)
		}
		netPolicy.AllowedCIDRs = cidrs
	} else {
		logger.Warn("ACS_DEVICE_NET_ALLOWED_CIDRS not set — the web-GUI proxy and console bridge may dial any non-loopback/non-metadata address the ACS host can reach. Set it to your device networks to close this down.")
	}

	// Same shape, different population (audit H-7): BSS webhook
	// target_url points at a receiver on the internet/BSS network, not a
	// CPE, so it gets its own policy/CIDR list rather than reusing
	// ACS_DEVICE_NET_ALLOWED_CIDRS.
	bssWebhookNetPolicy := netguard.Policy{}
	if v := os.Getenv("ACS_BSS_WEBHOOK_ALLOWED_CIDRS"); v != "" {
		cidrs, err := netguard.ParseCIDRList(v)
		if err != nil {
			logger.Error("invalid ACS_BSS_WEBHOOK_ALLOWED_CIDRS", "err", err)
			os.Exit(1)
		}
		bssWebhookNetPolicy.AllowedCIDRs = cidrs
	}

	h := &handler{
		logger:              logger,
		devices:             devices.NewRepository(db),
		jobs:                jobs.NewRepository(db),
		params:              parameters.NewRepository(db),
		vendors:             adapters.NewRegistry(),
		auditor:             observability.NewAuditor(db),
		firmware:            firmware.NewRepository(db),
		firmwareFS:          firmwareStorage,
		firmwareBase:        envOr("ACS_FIRMWARE_BASE_URL", "http://localhost:8080"),
		operators:           operators.NewRepository(db),
		jwtSecret:           jwtSecret,
		transferKey:         transferKey,
		uploadMaxBytes:      uploadMaxBytes,
		netPolicy:           netPolicy,
		bssWebhookNetPolicy: bssWebhookNetPolicy,
		metrics:             metrics,
		groups:              devices.NewGroupRepository(db),
		credentials:         credentialsRepo,
		schedules:           scheduler.NewRepository(db),
		rollouts:            rollout.NewRepository(db),
		policies:            policy.NewRepository(db),
		uploads:             uploads.NewRepository(db),
		uploadsFS:           uploadStorage,
		uploadsBase:         envOr("ACS_UPLOAD_BASE_URL", "http://localhost:8080"),
		templates:           templates.NewRepository(db),
		cli:                 cliRepo,
		permissions:         operators.NewPermissionRepository(db),
		mailer: mailer.New(mailer.Config{
			Host: os.Getenv("ACS_SMTP_HOST"), Port: envOr("ACS_SMTP_PORT", "587"),
			Username: os.Getenv("ACS_SMTP_USERNAME"), Password: os.Getenv("ACS_SMTP_PASSWORD"),
			From: os.Getenv("ACS_SMTP_FROM"),
		}, logger),
		frontendBaseURL: envOr("ACS_FRONTEND_BASE_URL", "http://localhost:5173"),
		tenancy:         tenancy.NewRepository(db),
		dashboards:      dashboard.NewRepository(db),

		bssMappings:     bss.NewRepository(db),
		bssWebhooks:     bss.NewWebhookRepository(db),
		bssOAuthClients: bss.NewOAuthRepository(db),
		bssAdapterURL:   envOr("ACS_BSS_ADAPTER_URL", "http://localhost:8090"),
		bssToken:        os.Getenv("ACS_BSS_API_TOKEN"),
		bssHTTPClient:   &http.Client{Timeout: 10 * time.Second},

		vpnPeers:        vpnRepo,
		vpnConcentrator: vpnConcentrator,
	}
	if h.bssToken == "" {
		logger.Warn("ACS_BSS_API_TOKEN not set — BSS admin-panel troubleshooting calls will hit the adapter unauthenticated (fine only if cmd/bssadapter also has no ACS_BSS_API_TOKEN set)")
	}

	if !h.mailer.Configured() {
		logger.Warn("ACS_SMTP_HOST not set — self-service password reset emails will be logged instead of sent (fine for development; set ACS_SMTP_HOST/PORT/USERNAME/PASSWORD/FROM for real delivery).")
	}

	if err := bootstrapAdmin(ctx, h, os.Getenv("ACS_BOOTSTRAP_ADMIN_USERNAME"), os.Getenv("ACS_BOOTSTRAP_ADMIN_PASSWORD")); err != nil {
		logger.Error("failed to bootstrap admin operator", "err", err)
		os.Exit(1)
	}

	mux := h.registerRoutes(metrics, db)

	connReqUsername := os.Getenv("ACS_CONNECTION_REQUEST_USERNAME")
	connReqPassword := os.Getenv("ACS_CONNECTION_REQUEST_PASSWORD")
	if connReqUsername == "" {
		logger.Warn("ACS_CONNECTION_REQUEST_USERNAME/ACS_CONNECTION_REQUEST_PASSWORD not set — Connection Request GETs will be sent unauthenticated, and a 401 challenge cannot be answered (design §5.6/§12).")
	}
	worker := &connectionRequestWorker{
		logger:      logger,
		jobs:        h.jobs,
		devices:     h.devices,
		credentials: h.credentials,
		auditor:     observability.NewAuditor(db),
		username:    connReqUsername,
		password:    connReqPassword,
		netPolicy:   netPolicy,
	}
	go worker.Run(ctx)

	scheduleW := &scheduleWorker{
		logger:    logger,
		schedules: h.schedules,
		jobs:      h.jobs,
		devices:   h.devices,
		groups:    h.groups,
		auditor:   observability.NewAuditor(db),
	}
	go scheduleW.Run(ctx)

	// Bounded growth for append-only tables (audit P2.3).
	go runRetention(ctx, db, logger)

	apiRate := envOrFloat("ACS_API_RATE_LIMIT_PER_SECOND", defaultAPIRateLimitPerSecond)
	apiBurst := envOrInt("ACS_API_RATE_LIMIT_BURST", defaultAPIRateLimitBurst)
	apiLimiter := ratelimit.New(apiRate, apiBurst, rateLimitIdleTTL)
	logger.Info("cmd/api rate limit configured", "per_second", apiRate, "burst", apiBurst)

	addr := envOr("ACS_API_ADDR", ":8080")
	// CORS (audit P1.5): default to the configured frontend origin, not
	// "*". A wildcard is still allowed for local experiments but is
	// called out loudly.
	corsOrigin := envOr("ACS_API_CORS_ORIGIN", h.frontendBaseURL)
	if corsOrigin == "*" {
		logger.Warn("ACS_API_CORS_ORIGIN=* — any web origin may call this API with a stolen token; set it to the console's exact origin in production")
	}
	server := &http.Server{
		Addr:    addr,
		Handler: withCORS(corsOrigin, withJWTAuth(jwtSecret, internalServiceToken, h.tokenCurrent, withRateLimit(apiLimiter, metrics, withBodyLimit(mux)))),
		// Timeouts (audit P1.2). No global ReadTimeout/WriteTimeout on
		// purpose: firmware uploads and CPE upload receipts stream large
		// bodies, and the console WebSocket / web-GUI proxy are long-lived
		// — a global write deadline would cut those off. Bodies are
		// bounded instead by withBodyLimit (JSON routes), MaxBytesReader
		// (upload receipt), and ParseMultipartForm's cap (firmware).
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("REST API listening", "addr", addr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		// Workers are already winding down (they share ctx). Give
		// in-flight requests a bounded window to finish, then exit.
		logger.Info("shutting down: draining in-flight requests")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Warn("shutdown did not complete cleanly", "err", err)
		}
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

func envOrInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

type handler struct {
	logger  *slog.Logger
	devices *devices.Repository
	jobs    *jobs.Repository
	params  *parameters.Repository
	vendors *adapters.Registry
	auditor *observability.Auditor

	firmware     *firmware.Repository
	firmwareFS   firmware.Storage
	firmwareBase string // base URL the CPE fetches Download URLs from (design doc v3 §9.4's "HTTPS static file host", local-disk stand-in — see internal/firmware doc comment)

	operators *operators.Repository
	jwtSecret []byte // empty means operator auth is disabled — see main()'s ACS_JWT_SIGNING_SECRET warning

	// transferKey signs the expiring tokens on public firmware-download
	// and upload-receipt URLs (audit P0.3, internal/transfer). Derived
	// from the JWT secret; empty (dev mode only) disables enforcement.
	transferKey    []byte
	uploadMaxBytes int64

	// netPolicy restricts where the web-GUI proxy and console bridge
	// may dial (audit P0.4) — see ACS_DEVICE_NET_ALLOWED_CIDRS.
	netPolicy netguard.Policy
	// bssWebhookNetPolicy restricts where a BSS webhook target_url may
	// point (audit H-7) — see ACS_BSS_WEBHOOK_ALLOWED_CIDRS.
	bssWebhookNetPolicy netguard.Policy

	// JWT revocation cache — see tokenCurrent.
	versionMu    sync.Mutex
	versionCache map[string]versionEntry

	metrics     *observability.Metrics
	groups      *devices.GroupRepository
	credentials *credentials.Repository
	schedules   *scheduler.Repository
	rollouts    *rollout.Repository
	policies    *policy.Repository

	uploads     *uploads.Repository
	uploadsFS   uploads.Storage
	uploadsBase string // base URL the CPE PUTs Upload'd files back to, mirrors firmwareBase

	templates       *templates.Repository
	cli             *cliaccess.Repository
	permissions     *operators.PermissionRepository
	mailer          *mailer.Mailer
	frontendBaseURL string
	tenancy         *tenancy.Repository
	dashboards      *dashboard.Repository

	bssMappings     *bss.Repository
	bssWebhooks     *bss.WebhookRepository
	bssOAuthClients *bss.OAuthRepository
	bssAdapterURL   string // base URL of the live cmd/bssadapter process, for admin-panel troubleshooting calls
	bssToken        string // same shared token bssadapter itself expects, so admin-panel troubleshooting calls authenticate as a real BSS caller would
	bssHTTPClient   *http.Client

	vpnPeers        *vpn.Repository
	vpnConcentrator vpn.ConcentratorConfig
}
