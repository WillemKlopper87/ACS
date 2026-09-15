// Command bssadapter is the BSS-facing gateway (build plan §5, Phase 8):
// account-device mapping, order dispatch, and job-status passthrough for
// BSS/CRM systems (Salesforce Comm Cloud, Amdocs, Netcracker, custom
// operator CRM). It never talks CWMP and never touches the jobs table —
// every write goes through the same internal ACS REST API an operator
// uses (internal/bss/acsclient.go), and it owns two tables of its own
// (account_device_mappings, bss_orders) in the same Postgres instance.
//
// This is a from-scratch implementation grounded in three reference
// documents (Design.txt, BSS integration guide.md,
// internal_bss_adapter.go) but not a copy of the draft — see build plan
// §5 for the specific gaps that draft had (in-memory mapping storage, no
// order idempotency, an unsafe SUSPEND template) and how this fixes them.
package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"acs/internal/alerting"
	"acs/internal/auth"
	"acs/internal/bss"
	"acs/internal/config"
	"acs/internal/netguard"
	"acs/internal/observability"
	"acs/internal/ratelimit"
	"acs/internal/store"
)

// bssClientRole is the synthetic role stamped on an OAuth2 client's
// issued JWT — bss_admin_handlers.go and this package don't have a real
// operator-role concept, this just labels the claims for logging/audit
// purposes.
const bssClientRole = "bss_client"

// maxBodyBytes caps every /bss/v1 request body — mappings/orders are small
// JSON payloads, well under cmd/acs's 4 MiB CWMP allowance (build plan §4
// Phase 8 open items: this surface wasn't capped when cmd/acs already was).
const maxBodyBytes = 1 << 20 // 1 MiB

// Rate limit defaults (build plan §7.4 sub-phase 7b: "Per-token rate
// limiting on cmd/bssadapter, highest exposure, auth already exists").
// Generous enough for a legitimate BSS integration submitting orders in
// normal operation, tight enough to catch one misbehaving/retry-looping
// caller — tune via env, not meant as a load-tested production number.
const (
	defaultRateLimitPerSecond = 5
	defaultRateLimitBurst     = 10
	rateLimitIdleTTL          = 10 * time.Minute
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	dsn := os.Getenv("ACS_POSTGRES_DSN")
	if dsn == "" {
		logger.Error("ACS_POSTGRES_DSN is required")
		os.Exit(1)
	}
	// Fail-closed auth enforcement (audit P0.1): inbound /bss/v1 calls
	// need OAuth or the legacy shared token, and outbound calls to
	// cmd/api need the internal service token. The historical
	// unauthenticated mode now requires ACS_INSECURE_DEV_MODE=true.
	if err := config.RequireOneOf(logger, "the /bss/v1 endpoints would otherwise accept unauthenticated callers",
		config.Secret{Env: "ACS_BSS_OAUTH_SIGNING_SECRET", MinBytes: 32, Purpose: "signs OAuth2 client-credentials tokens for BSS integrations"},
		config.Secret{Env: "ACS_BSS_API_TOKEN", MinBytes: 16, Purpose: "legacy shared bearer token for BSS callers"},
	); err != nil {
		logger.Error("refusing to start", "err", err)
		os.Exit(1)
	}
	adapterSecrets := []config.Secret{
		{Env: "ACS_INTERNAL_SERVICE_TOKEN", MinBytes: 32, Purpose: "authenticates this adapter's calls into cmd/api (order dispatch, job status)"},
	}
	if err := config.Validate(logger, adapterSecrets...); err != nil {
		logger.Error("refusing to start", "err", err)
		os.Exit(1)
	}
	config.LogSummary(logger,
		config.Secret{Env: "ACS_BSS_OAUTH_SIGNING_SECRET"},
		config.Secret{Env: "ACS_BSS_API_TOKEN"},
		config.Secret{Env: "ACS_INTERNAL_SERVICE_TOKEN"},
	)

	acsBaseURL := envOr("ACS_INTERNAL_API_URL", "http://localhost:8080")
	token := os.Getenv("ACS_BSS_API_TOKEN")
	oauthSigningSecret := []byte(os.Getenv("ACS_BSS_OAUTH_SIGNING_SECRET"))
	if token == "" && len(oauthSigningSecret) == 0 {
		logger.Warn("Neither ACS_BSS_API_TOKEN nor ACS_BSS_OAUTH_SIGNING_SECRET is set — /bss/v1 endpoints are running WITHOUT authentication. Lab use only.")
	}
	if len(oauthSigningSecret) == 0 {
		logger.Warn("ACS_BSS_OAUTH_SIGNING_SECRET not set — the OAuth2 client-credentials token endpoint (POST /bss/v1/oauth/token) is disabled; only the legacy shared ACS_BSS_API_TOKEN works. Set this to move BSS integrations onto real per-integration credentials.")
	}
	if token != "" {
		logger.Warn("ACS_BSS_API_TOKEN is set — the legacy shared-token auth path is still accepted alongside OAuth2. This is deprecated: register real OAuth2 clients (BSS admin panel) and unset this once every integration has migrated.")
	}
	// The credential this process presents *to* cmd/api (distinct from
	// ACS_BSS_API_TOKEN above, which is what BSS callers present *to this
	// process*) — see cmd/api's withJWTAuth doc comment for why bssadapter
	// needs its own machine credential rather than an operator login.
	internalServiceToken := os.Getenv("ACS_INTERNAL_SERVICE_TOKEN")
	if internalServiceToken == "" {
		logger.Warn("ACS_INTERNAL_SERVICE_TOKEN not set — order dispatch and job status lookups will get 401'd by cmd/api once its own operator JWT auth is enabled. Set the same value here and on cmd/api's ACS_INTERNAL_SERVICE_TOKEN.")
	}

	// Process lifecycle (audit P1.2): SIGINT/SIGTERM cancels ctx, which
	// stops the webhook workers, then the HTTP server drains.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, dsn)
	if err != nil {
		logger.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	metrics := observability.NewMetrics("bssadapter")
	metrics.ObserveDB(db)

	walledGarden := bss.WalledGardenConfig{
		Parameter:    os.Getenv("ACS_WALLED_GARDEN_PARAMETER"),
		SuspendValue: os.Getenv("ACS_WALLED_GARDEN_SUSPEND_VALUE"),
		ActiveValue:  os.Getenv("ACS_WALLED_GARDEN_ACTIVE_VALUE"),
	}
	if walledGarden.Parameter == "" {
		logger.Warn("ACS_WALLED_GARDEN_PARAMETER not set — SUSPEND/ACTIVATE orders will be rejected (build plan §5.3: no universal safe parameter across CPE vendors, so this isn't guessed at).")
	}

	// Webhook target_url is BSS-operator-controlled (audit H-7): without a
	// policy, a subscription could aim signed POSTs at the cloud metadata
	// service or any internal host this process can reach. Empty means no
	// allowlist beyond the always-forbidden classes (loopback/link-local/
	// multicast) netguard refuses unconditionally — same safe-by-default
	// shape as ACS_DEVICE_NET_ALLOWED_CIDRS in cmd/api.
	webhookNetPolicy := netguard.Policy{}
	if v := os.Getenv("ACS_BSS_WEBHOOK_ALLOWED_CIDRS"); v != "" {
		cidrs, err := netguard.ParseCIDRList(v)
		if err != nil {
			logger.Error("invalid ACS_BSS_WEBHOOK_ALLOWED_CIDRS", "err", err)
			os.Exit(1)
		}
		webhookNetPolicy.AllowedCIDRs = cidrs
	}

	h := &handler{
		logger:             logger,
		mappings:           bss.NewRepository(db),
		acs:                bss.NewACSClient(acsBaseURL, 10*time.Second, internalServiceToken),
		auditor:            observability.NewAuditor(db),
		token:              token,
		oauthClients:       bss.NewOAuthRepository(db),
		oauthSigningSecret: oauthSigningSecret,
		metrics:            metrics,
		walledGarden:       walledGarden,
		webhooks:           bss.NewWebhookRepository(db),
		alertPolicies:      alerting.NewRepository(db),
		alertIncidents:     alerting.NewIncidentRepository(db),
		netPolicy:          webhookNetPolicy,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", metrics.Handler().ServeHTTP)
	mux.Handle("GET /healthz", observability.LivenessHandler())
	mux.Handle("GET /readyz", observability.ReadinessHandler(db))
	mux.HandleFunc("POST /bss/v1/oauth/token", metrics.InstrumentHTTP("POST /bss/v1/oauth/token", h.issueOAuthToken))
	mux.HandleFunc("POST /bss/v1/mappings", metrics.InstrumentHTTP("POST /bss/v1/mappings", h.createMapping))
	mux.HandleFunc("GET /bss/v1/mappings/{account_id}", metrics.InstrumentHTTP("GET /bss/v1/mappings/{account_id}", h.listMappings))
	mux.HandleFunc("POST /bss/v1/orders", metrics.InstrumentHTTP("POST /bss/v1/orders", h.createOrder))
	mux.HandleFunc("GET /bss/v1/jobs/{command_key}", metrics.InstrumentHTTP("GET /bss/v1/jobs/{command_key}", h.getJob))
	mux.HandleFunc("POST /bss/v1/webhooks", metrics.InstrumentHTTP("POST /bss/v1/webhooks", h.createWebhookSubscription))
	mux.HandleFunc("GET /bss/v1/webhooks", metrics.InstrumentHTTP("GET /bss/v1/webhooks", h.listWebhookSubscriptions))
	mux.HandleFunc("DELETE /bss/v1/webhooks/{id}", metrics.InstrumentHTTP("DELETE /bss/v1/webhooks/{id}", h.deleteWebhookSubscription))
	mux.HandleFunc("GET /tmf-api/serviceInventoryManagement/v4/service", metrics.InstrumentHTTP("GET /tmf-api/serviceInventoryManagement/v4/service", h.listTMF638Services))
	mux.HandleFunc("GET /tmf-api/serviceInventoryManagement/v4/service/{id}", metrics.InstrumentHTTP("GET /tmf-api/serviceInventoryManagement/v4/service/{id}", h.getTMF638Service))
	mux.HandleFunc("POST /tmf-api/serviceProblemManagement/v4/serviceProblem", metrics.InstrumentHTTP("POST /tmf-api/serviceProblemManagement/v4/serviceProblem", h.createTMF656Problem))
	mux.HandleFunc("GET /tmf-api/serviceProblemManagement/v4/serviceProblem/{id}", metrics.InstrumentHTTP("GET /tmf-api/serviceProblemManagement/v4/serviceProblem/{id}", h.getTMF656Problem))
	mux.HandleFunc("GET /tmf-api/serviceProblemManagement/v4/serviceProblem", metrics.InstrumentHTTP("GET /tmf-api/serviceProblemManagement/v4/serviceProblem", h.listTMF656Problems))
	mux.HandleFunc("PATCH /tmf-api/serviceProblemManagement/v4/serviceProblem/{id}", metrics.InstrumentHTTP("PATCH /tmf-api/serviceProblemManagement/v4/serviceProblem/{id}", h.patchTMF656Problem))
	mux.HandleFunc("POST /tmf-api/eventManagement/v4/event", metrics.InstrumentHTTP("POST /tmf-api/eventManagement/v4/event", h.createTMF688Event))
	mux.HandleFunc("POST /tmf-api/alarmManagement/v4/alarm", metrics.InstrumentHTTP("POST /tmf-api/alarmManagement/v4/alarm", h.createTMF642Alarm))
	mux.HandleFunc("GET /tmf-api/eventManagement/v4/event/{id}", metrics.InstrumentHTTP("GET /tmf-api/eventManagement/v4/event/{id}", h.getTMF688Event))
	mux.HandleFunc("GET /tmf-api/alarmManagement/v4/alarm/{id}", metrics.InstrumentHTTP("GET /tmf-api/alarmManagement/v4/alarm/{id}", h.getTMF642Alarm))
	mux.HandleFunc("GET /tmf-api/eventManagement/v4/event", metrics.InstrumentHTTP("GET /tmf-api/eventManagement/v4/event", h.listTMF688Events))
	mux.HandleFunc("GET /tmf-api/alarmManagement/v4/alarm", metrics.InstrumentHTTP("GET /tmf-api/alarmManagement/v4/alarm", h.listTMF642Alarms))
	mux.HandleFunc("POST /tmf-api/eventManagement/v4/hub", metrics.InstrumentHTTP("POST /tmf-api/eventManagement/v4/hub", h.createTMF688Hub))
	mux.HandleFunc("GET /tmf-api/eventManagement/v4/hub", metrics.InstrumentHTTP("GET /tmf-api/eventManagement/v4/hub", h.listTMF688Hubs))
	mux.HandleFunc("PATCH /tmf-api/alarmManagement/v4/alarm/{id}", metrics.InstrumentHTTP("PATCH /tmf-api/alarmManagement/v4/alarm/{id}", h.patchTMF642Alarm))
	mux.HandleFunc("POST /tmf-api/serviceOrdering/v4/serviceOrder", metrics.InstrumentHTTP("POST /tmf-api/serviceOrdering/v4/serviceOrder", h.createTMF641Order))
	mux.HandleFunc("GET /tmf-api/serviceOrdering/v4/serviceOrder/{id}", metrics.InstrumentHTTP("GET /tmf-api/serviceOrdering/v4/serviceOrder/{id}", h.getTMF641Order))
	mux.HandleFunc("PATCH /tmf-api/serviceOrdering/v4/serviceOrder/{id}", metrics.InstrumentHTTP("PATCH /tmf-api/serviceOrdering/v4/serviceOrder/{id}", h.cancelTMF641Order))
	mux.HandleFunc("GET /tmf-api/serviceOrdering/v4/serviceOrder", metrics.InstrumentHTTP("GET /tmf-api/serviceOrdering/v4/serviceOrder", h.listTMF641Orders))

	go h.runWebhookNotifyLoop(ctx)
	go h.runWebhookDeliverLoop(ctx)
	go h.runTMFEventDispatchLoop(ctx)
	go h.runIncidentIngestLoop(ctx)
	go h.runOfflineIncidentLoop(ctx)
	go h.runEscalationLoop(ctx)
	go h.runOrderReconcileLoop(ctx)

	rateLimitPerSecond := envOrFloat("ACS_BSS_RATE_LIMIT_PER_SECOND", defaultRateLimitPerSecond)
	rateLimitBurst := envOrInt("ACS_BSS_RATE_LIMIT_BURST", defaultRateLimitBurst)
	limiter := ratelimit.New(rateLimitPerSecond, rateLimitBurst, rateLimitIdleTTL)

	addr := envOr("ACS_BSS_ADDR", ":8090")
	server := &http.Server{
		Addr: addr,
		// Order matters: auth first, so a request with an invalid or
		// missing token is rejected (401) without ever touching the rate
		// limiter — otherwise an attacker could spray distinct bogus
		// tokens to dodge a per-token bucket entirely.
		Handler:           withAuth(token, oauthSigningSecret, h.clientRevoked, withRateLimit(limiter, metrics, withMaxBody(mux))),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second, // every BSS route is a small JSON exchange
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// mTLS (secondary hardening layer alongside OAuth2 — mirrors cmd/acs's
	// CWMP mTLS setup exactly: optional, and additive rather than a
	// replacement for the app-layer bearer check above. A client cert
	// verified here proves the *transport* is talking to a holder of a
	// CA-issued cert; withAuth still separately checks the OAuth2/legacy
	// bearer token, so the two layers compose instead of one replacing
	// the other.
	certFile := os.Getenv("ACS_BSS_TLS_CERT")
	keyFile := os.Getenv("ACS_BSS_TLS_KEY")
	if caCertFile := os.Getenv("ACS_BSS_MTLS_CA_CERT"); caCertFile != "" {
		caCert, err := os.ReadFile(caCertFile)
		if err != nil {
			logger.Error("failed to read ACS_BSS_MTLS_CA_CERT", "err", err, "path", caCertFile)
			os.Exit(1)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			logger.Error("ACS_BSS_MTLS_CA_CERT did not contain a valid PEM certificate", "path", caCertFile)
			os.Exit(1)
		}
		server.TLSConfig = &tls.Config{
			ClientAuth: tls.VerifyClientCertIfGiven,
			ClientCAs:  pool,
		}
		logger.Info("mTLS enabled for bssadapter — client certs verified against ACS_BSS_MTLS_CA_CERT when presented, OAuth2/legacy bearer auth still required", "ca_cert", caCertFile)
	}

	logger.Info("BSS adapter listening", "addr", addr, "acs_internal_api", acsBaseURL,
		"rate_limit_per_second", rateLimitPerSecond, "rate_limit_burst", rateLimitBurst,
		"tls", certFile != "" && keyFile != "")
	if certFile == "" && server.TLSConfig != nil {
		logger.Error("ACS_BSS_MTLS_CA_CERT is set but ACS_BSS_TLS_CERT/ACS_BSS_TLS_KEY are not — mTLS needs the server to actually be running TLS")
		os.Exit(1)
	}
	errCh := make(chan error, 1)
	go func() {
		if certFile != "" && keyFile != "" {
			errCh <- server.ListenAndServeTLS(certFile, keyFile)
		} else {
			errCh <- server.ListenAndServe()
		}
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
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

// withAuth enforces authentication on every /bss/v1 request except the
// token endpoint itself (which has its own client-credentials check) and
// /metrics. Two credential types are accepted, checked in this order:
//  1. An OAuth2 bearer JWT issued by POST /bss/v1/oauth/token — the
//     production-grade path (RFC 6749 §4.4), verified against
//     oauthSigningSecret. This is what a real per-integration BSS client
//     should use.
//  2. The legacy shared token — kept working for backward compatibility
//     during migration, but deprecated (see main()'s startup warning).
//
// Both are "off unless configured": if neither oauthSigningSecret nor
// token is set, every request passes — same lab-mode default as before.
//
// revoked (audit P2.3) is checked on every OAuth-client-issued token,
// not just at issuance: JWT verification alone only proves the token
// was validly signed and hasn't expired (up to oauthTokenTTL, one
// hour), which would otherwise let a token issued just before an
// operator revokes a compromised client keep working for the rest of
// that hour. Chose active revocation checking over the document's other
// options (shortening the TTL further, or accepting the residual
// window) because it closes the gap outright rather than just bounding
// it, and the check is cheap — see clientRevoked's short cache.
func withAuth(token string, oauthSigningSecret []byte, revoked func(context.Context, string) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (token == "" && len(oauthSigningSecret) == 0) ||
			(r.Method == http.MethodGet && r.URL.Path == "/metrics") ||
			(r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz")) ||
			(r.Method == http.MethodPost && r.URL.Path == "/bss/v1/oauth/token") {
			next.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("Authorization")

		if len(oauthSigningSecret) > 0 {
			if bearer, ok := strings.CutPrefix(got, "Bearer "); ok {
				if claims, err := auth.VerifyJWT(oauthSigningSecret, bearer); err == nil && claims.Role == bssClientRole {
					clientID := strings.TrimPrefix(claims.Subject, "bss-client:")
					if !revoked(r.Context(), clientID) {
						next.ServeHTTP(w, r)
						return
					}
					writeError(w, http.StatusUnauthorized, "ErrUnauthorized", "oauth client has been revoked")
					return
				}
			}
		}

		if token != "" && subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+token)) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		writeError(w, http.StatusUnauthorized, "ErrUnauthorized", "missing or invalid Authorization header")
	})
}

// withRateLimit enforces a per-caller token bucket (build plan §7.3/§7b).
// Runs after withAuth, so the key is the (already-verified) Authorization
// header — every legitimately-authenticated caller shares one bucket
// under today's single-shared-token model; that becomes genuinely
// per-integration once individual BSS tokens exist. When auth itself is
// disabled (no ACS_BSS_API_TOKEN configured — lab mode), there's no token
// to key on, so this falls back to remote address as a coarser
// defense-in-depth limit.
func withRateLimit(limiter *ratelimit.Limiter, metrics *observability.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		if key == "" {
			key = r.RemoteAddr
		}
		if !limiter.Allow(key) {
			metrics.RateLimitRejectedTotal.Inc()
			writeError(w, http.StatusTooManyRequests, "ErrRateLimited", "rate limit exceeded, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withMaxBody rejects any /bss/v1 request body over maxBodyBytes before it
// reaches a handler's json.Decoder, the same guard cmd/acs's CWMP endpoint
// already has against oversized payloads.
func withMaxBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

type handler struct {
	logger             *slog.Logger
	mappings           *bss.Repository
	acs                *bss.ACSClient
	auditor            *observability.Auditor
	token              string
	oauthClients       *bss.OAuthRepository
	oauthSigningSecret []byte
	metrics            *observability.Metrics
	walledGarden       bss.WalledGardenConfig
	webhooks           *bss.WebhookRepository
	alertPolicies      *alerting.Repository
	alertIncidents     *alerting.IncidentRepository
	// netPolicy bounds where a webhook target_url (BSS-operator-
	// controlled) may point (audit H-7) -- checked at subscription
	// creation and again at delivery time.
	netPolicy netguard.Policy

	// revocationMu/revocationCache back clientRevoked (audit P2.3): a
	// short-TTL cache of oauth_clients.revoked_at, keyed by client_id, so
	// checking it on every request costs one Postgres lookup per client
	// per TTL rather than per request.
	revocationMu    sync.Mutex
	revocationCache map[string]revocationEntry
}

type revocationEntry struct {
	revoked bool
	expires time.Time
}

// revocationCacheTTL bounds how long a just-revoked OAuth client's
// already-issued tokens keep working after the revocation call — the
// same shape and TTL as cmd/api's operator token_version cache.
const revocationCacheTTL = 15 * time.Second

// clientRevoked reports whether clientID's OAuth client has been
// revoked (audit P2.3). Fails closed: a lookup error is treated as
// revoked, so a Postgres blip can't resurrect a revoked client's access.
func (h *handler) clientRevoked(ctx context.Context, clientID string) bool {
	h.revocationMu.Lock()
	entry, ok := h.revocationCache[clientID]
	h.revocationMu.Unlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.revoked
	}

	revoked, err := h.oauthClients.IsRevoked(ctx, clientID)
	if err != nil {
		h.logger.Error("oauth client revocation check failed", "err", err, "client_id", clientID)
		return true
	}
	entry = revocationEntry{revoked: revoked, expires: time.Now().Add(revocationCacheTTL)}
	h.revocationMu.Lock()
	if h.revocationCache == nil {
		h.revocationCache = map[string]revocationEntry{}
	}
	h.revocationCache[clientID] = entry
	h.revocationMu.Unlock()
	return revoked
}

// errorEnvelope matches the BSS integration guide §4 error shape.
type errorEnvelope struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// --- Workflow A: mappings -------------------------------------------------

type createMappingRequest struct {
	AccountID   string `json:"account_id"`
	OUISerial   string `json:"oui_serial"`
	DeviceUUID  string `json:"device_uuid"`
	ServicePlan string `json:"service_plan"`
	Role        string `json:"role"` // optional; defaults to gateway
}

type mappingResponse struct {
	AccountID   string `json:"account_id"`
	DeviceUUID  string `json:"device_uuid"`
	OUISerial   string `json:"oui_serial"`
	ServicePlan string `json:"service_plan,omitempty"`
	Status      string `json:"status"`
	Role        string `json:"role"`
}

// roleOrDefault applies the contract rule for callers that name no role:
// they target the gateway. An account with no gateway assigned gets a
// clean ErrNoDeviceForRole rather than an arbitrary device.
func roleOrDefault(role string) string {
	if role == "" {
		return bss.RoleGateway
	}
	return role
}

// createMapping implements Workflow A. Unlike the reference draft, it
// resolves oui_serial against the real devices table rather than trusting
// the caller's device_uuid outright — if the caller supplied one, it must
// match what oui_serial actually resolves to.
func (h *handler) createMapping(w http.ResponseWriter, r *http.Request) {
	var req createMappingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "invalid JSON body")
		return
	}
	if req.AccountID == "" || req.OUISerial == "" {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "account_id and oui_serial are required")
		return
	}

	// Resolve and validate device_uuid *before* writing anything. Doing
	// this after AssignDevice used to commit the row, then reject the
	// caller with a 400 that left the role slot wedged — their corrected
	// retry got 409 ErrRoleAlreadyAssigned for a mapping this handler
	// itself created. See internal/bss.Repository.DeviceIDForSerial.
	if req.DeviceUUID != "" {
		resolvedID, err := h.mappings.DeviceIDForSerial(r.Context(), req.OUISerial)
		if errors.Is(err, bss.ErrDeviceNotFound) {
			writeError(w, http.StatusNotFound, "ErrDeviceNotMapped", err.Error())
			return
		}
		if err != nil {
			h.logger.Error("failed to resolve device for mapping", "err", err, "account_id", req.AccountID)
			writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
			return
		}
		if req.DeviceUUID != resolvedID {
			writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "device_uuid does not match the device resolved from oui_serial")
			return
		}
	}

	mapping, err := h.mappings.AssignDevice(r.Context(), req.AccountID, req.OUISerial, roleOrDefault(req.Role), req.ServicePlan)
	if errors.Is(err, bss.ErrDeviceNotFound) {
		writeError(w, http.StatusNotFound, "ErrDeviceNotMapped", err.Error())
		return
	}
	if errors.Is(err, bss.ErrInvalidRole) || errors.Is(err, bss.ErrInvalidUnassignReason) {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", err.Error())
		return
	}
	if errors.Is(err, bss.ErrDeviceAlreadyAssigned) {
		writeError(w, http.StatusConflict, "ErrDeviceAlreadyAssigned", "this device is already actively assigned to the account, under some role")
		return
	}
	if errors.Is(err, bss.ErrRoleAlreadyAssigned) {
		writeError(w, http.StatusConflict, "ErrRoleAlreadyAssigned", "the account already has an active device in that role; unassign or swap it first")
		return
	}
	if err != nil {
		h.logger.Error("failed to create mapping", "err", err, "account_id", req.AccountID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	if err := h.auditor.Record(r.Context(), "bss:"+req.AccountID, mapping.DeviceID, "BSSMappingCreated", map[string]any{
		"account_id": req.AccountID, "oui_serial": req.OUISerial, "service_plan": req.ServicePlan, "role": mapping.Role,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("mapping created", "account_id", req.AccountID, "device_id", mapping.DeviceID)

	writeJSON(w, http.StatusOK, mappingResponse{
		AccountID: mapping.AccountID, DeviceUUID: mapping.DeviceID, OUISerial: mapping.OUISerial,
		ServicePlan: mapping.ServicePlan, Status: mapping.Status, Role: mapping.Role,
	})
}

func (h *handler) listMappings(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("account_id")
	list, err := h.mappings.ListByAccount(r.Context(), accountID)
	if err != nil {
		h.logger.Error("failed to list mappings", "err", err, "account_id", accountID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	items := make([]mappingResponse, 0, len(list))
	for _, m := range list {
		items = append(items, mappingResponse{
			AccountID: m.AccountID, DeviceUUID: m.DeviceID, OUISerial: m.OUISerial,
			ServicePlan: m.ServicePlan, Status: m.Status, Role: m.Role,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// --- Workflow B: orders ----------------------------------------------------

type createOrderRequest struct {
	ExternalOrderID string            `json:"external_order_id"`
	AccountID       string            `json:"account_id"`
	ServiceType     string            `json:"service_type"`
	Action          string            `json:"action"`
	Role            string            `json:"role"` // optional; defaults to gateway
	Parameters      map[string]string `json:"parameters"`
}

type orderResponse struct {
	OrderTrackingID string    `json:"order_tracking_id"`
	CommandKey      string    `json:"command_key"`
	Status          string    `json:"status"`
	Timestamp       time.Time `json:"timestamp"`
}

// respondWithExistingOrder answers with an already-recorded order's
// current status -- the idempotent response createOrder owes a caller
// that's asking about an order it (or a concurrent request racing it)
// has already recorded, whether that's an ordinary retried
// external_order_id (the ordinary FindOrder-at-top-of-function path
// below) or a genuine InsertPending race the loser learns about via
// bss.ErrOrderAlreadyExists (final review finding 6) -- both cases boil
// down to "an order with this external_order_id already exists; report
// it instead of dispatching a second one," so both share this one
// response path rather than duplicating it. Returns false only if it
// could not produce the response (a further failure resolving the
// order's job status), in which case it has already written the error
// response itself.
func (h *handler) respondWithExistingOrder(w http.ResponseWriter, r *http.Request, externalOrderID string, existing *bss.OrderRecord) bool {
	if existing.Status != bss.OrderStatusDispatched {
		// Still PENDING_DISPATCH or DEAD_LETTERED -- there is no
		// command_key to poll cmd/api for yet. Report the order's own
		// internal status directly instead of trying (and failing) to
		// look up a job that was never created.
		writeJSON(w, http.StatusAccepted, orderResponse{
			OrderTrackingID: externalOrderID, CommandKey: "",
			Status: existing.Status, Timestamp: time.Now().UTC(),
		})
		return true
	}
	status, err := h.acs.GetJobStatus(r.Context(), existing.CommandKey)
	if err != nil {
		h.logger.Error("failed to fetch status for existing order", "err", err, "command_key", existing.CommandKey)
		writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
		return false
	}
	writeJSON(w, http.StatusAccepted, orderResponse{
		OrderTrackingID: externalOrderID, CommandKey: existing.CommandKey,
		Status: status.Status, Timestamp: time.Now().UTC(),
	})
	return true
}

// dispatchOrder runs the outbox's write-ahead-then-dispatch sequence
// (design docs/superpowers/specs/2026-09-13-bss-improvements-design.md
// S3): InsertPending, SetParameters, then Mark(Dispatched|Failed) plus
// the audit record and log line -- extracted here as the ONE place this
// sequence exists (design docs/superpowers/specs/
// 2026-09-13-bss-tmf640-design.md §4.2), shared by createOrder
// (/bss/v1/orders) and patchService (TMF640's PATCH /service/{id}, added
// in a later task in this same plan) so neither path can drift from the
// other's outbox semantics.
//
// On a genuine InsertPending race (bss.ErrOrderAlreadyExists returned
// unwrapped, per errors.Is), the caller is responsible for routing to
// its own idempotent-response path -- this function only dispatches, it
// does not know how to shape either caller's response.
func (h *handler) dispatchOrder(ctx context.Context, externalOrderID, accountID, action, deviceID string, params []bss.ParameterWrite) (commandKey string, err error) {
	// Write-ahead: the order's intent, including exactly what dispatch
	// would send, is durable before dispatch is attempted (design S3).
	if err := h.mappings.InsertPending(ctx, externalOrderID, accountID, action, deviceID, params); err != nil {
		// ErrOrderAlreadyExists is the expected idempotent-retry race --
		// the caller routes it to its own existing-order response, not
		// an error worth logging here. Any other InsertPending failure
		// (e.g. a DB connectivity error) never reached ACS at all, so it
		// gets its own log line distinct from a dispatch failure below.
		if !errors.Is(err, bss.ErrOrderAlreadyExists) {
			h.logger.Error("failed to record pending order", "err", err, "external_order_id", externalOrderID)
		}
		return "", err
	}

	commandKey, err = h.acs.SetParameters(ctx, deviceID, params)
	if err != nil {
		if markErr := h.mappings.MarkDispatchFailed(ctx, externalOrderID, err.Error()); markErr != nil {
			h.logger.Error("failed to record dispatch failure", "err", markErr, "external_order_id", externalOrderID)
		}
		// ErrACSUnreachable maps to its own 502 at the caller with no
		// extra logging there; any other SetParameters failure is logged
		// here (with the context a caller-side generic branch wouldn't
		// have without re-deriving it) before the caller turns it into a
		// generic 500.
		if !errors.Is(err, bss.ErrACSUnreachable) {
			h.logger.Error("failed to dispatch order to ACS", "err", err, "account_id", accountID, "action", action)
		}
		return "", err
	}

	// Dispatch succeeded. If this update itself fails, the job IS already
	// queued on the ACS side but the row stays PENDING_DISPATCH with no
	// command_key recorded -- order_reconciler.go will retry SetParameters
	// for it, which can double-dispatch in this narrow window (design S3's
	// disclosed residual: one UPDATE statement wide, not the open-ended
	// "any future BSS retry" window this design replaces).
	if err := h.mappings.MarkDispatched(ctx, externalOrderID, commandKey); err != nil {
		h.logger.Error("failed to record order dispatched -- reconciler retry may double-dispatch",
			"err", err, "external_order_id", externalOrderID, "command_key", commandKey)
	}

	if err := h.auditor.Record(ctx, "bss:"+accountID, deviceID, "BSSOrderDispatched", map[string]any{
		"external_order_id": externalOrderID, "action": action, "command_key": commandKey,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("order dispatched", "external_order_id", externalOrderID, "account_id", accountID,
		"action", action, "command_key", commandKey)

	return commandKey, nil
}

// createOrder implements Workflow B, idempotently: a retried
// external_order_id is answered from bss_orders (with the order's
// *current* status, not a stale "QUEUED") instead of dispatching a
// second job. The order's intent -- including exactly what dispatch
// would send -- is written to bss_orders BEFORE SetParameters is called
// (design S3's write-ahead outbox), so a crash or failure after that
// point leaves a durable row order_reconciler.go can retry, rather than
// nothing being written at all (the gap build plan §5.3 flagged in the
// reference draft, closed by this outbox and described in
// bss-integration-guide.md §6). The one residual outbox gap that
// remains -- a crash between SetParameters succeeding and that success
// being recorded -- is disclosed there and in design docs/superpowers/
// specs/2026-09-13-bss-improvements-design.md §3; it is not the same
// thing this comment used to call a "known limitation," which was the
// double-dispatch race closed by ClaimDuePendingOrders (internal/bss/
// order.go) and InsertPending's own last_attempt_at stamp. The write-ahead-then-dispatch sequence itself now lives in dispatchOrder, shared with TMF640's PATCH /service/{id} handler.
func (h *handler) createOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "invalid JSON body")
		return
	}
	if req.ExternalOrderID == "" || req.AccountID == "" || req.Action == "" {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "external_order_id, account_id, and action are required")
		return
	}

	if existing, err := h.mappings.FindOrder(r.Context(), req.ExternalOrderID); err != nil {
		h.logger.Error("failed to check order idempotency", "err", err, "external_order_id", req.ExternalOrderID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	} else if existing != nil {
		h.respondWithExistingOrder(w, r, req.ExternalOrderID, existing)
		return
	}

	mapping, err := h.mappings.ActiveDeviceForAccount(r.Context(), req.AccountID, roleOrDefault(req.Role))
	if errors.Is(err, bss.ErrNoDeviceForRole) {
		writeError(w, http.StatusNotFound, "ErrDeviceNotMapped", "no active device is assigned to this account in the requested role")
		return
	}
	if err != nil {
		h.logger.Error("failed to resolve account device", "err", err, "account_id", req.AccountID, "role", roleOrDefault(req.Role))
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	// Only MODIFY_WIFI's canonical WiFi paths depend on the device's data
	// model root (build plan §10's data_model_root branching gap) —
	// SUSPEND/ACTIVATE write a deployer-configured walled-garden
	// parameter directly, so they don't pay for this extra internal-API
	// round-trip or gain a new failure mode they didn't have before.
	dataModelRoot := ""
	if req.Action == "MODIFY_WIFI" {
		dev, err := h.acs.GetDevice(r.Context(), mapping.DeviceID)
		if errors.Is(err, bss.ErrACSUnreachable) {
			writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
			return
		}
		if err != nil {
			h.logger.Error("failed to resolve device for order translation", "err", err, "device_id", mapping.DeviceID)
			writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
			return
		}
		dataModelRoot = dev.DataModelRoot
	}

	params, err := bss.Translate(req.Action, req.Parameters, h.walledGarden, dataModelRoot)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", err.Error())
		return
	}

	commandKey, err := h.dispatchOrder(r.Context(), req.ExternalOrderID, req.AccountID, req.Action, mapping.DeviceID, params)
	if err != nil {
		if errors.Is(err, bss.ErrOrderAlreadyExists) {
			// A genuine race: another request for the same
			// external_order_id won InsertPending between our own
			// FindOrder check above and dispatchOrder's own call (final
			// review finding 6). The loser should get the same idempotent
			// answer a retried external_order_id gets, not a 500.
			existing, findErr := h.mappings.FindOrder(r.Context(), req.ExternalOrderID)
			if findErr != nil || existing == nil {
				h.logger.Error("failed to resolve order after InsertPending race", "err", findErr, "external_order_id", req.ExternalOrderID)
				writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
				return
			}
			h.respondWithExistingOrder(w, r, req.ExternalOrderID, existing)
			return
		}
		if errors.Is(err, bss.ErrACSUnreachable) {
			writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
			return
		}
		// Any other failure was already logged inside dispatchOrder,
		// which knows which step (InsertPending vs SetParameters) it
		// came from -- this branch only shapes the HTTP response.
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	writeJSON(w, http.StatusAccepted, orderResponse{
		OrderTrackingID: req.ExternalOrderID, CommandKey: commandKey, Status: "QUEUED", Timestamp: time.Now().UTC(),
	})
}

// --- Workflow C: job status -------------------------------------------------

func (h *handler) getJob(w http.ResponseWriter, r *http.Request) {
	commandKey := r.PathValue("command_key")
	status, err := h.acs.GetJobStatus(r.Context(), commandKey)
	if errors.Is(err, bss.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "ErrJobNotFound", "no job with that command_key")
		return
	}
	if errors.Is(err, bss.ErrACSUnreachable) {
		writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
		return
	}
	if err != nil {
		h.logger.Error("failed to fetch job status", "err", err, "command_key", commandKey)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, status)
}
