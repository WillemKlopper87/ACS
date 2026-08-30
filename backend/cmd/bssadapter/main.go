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
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"acs/internal/auth"
	"acs/internal/bss"
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

	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		logger.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	metrics := observability.NewMetrics("bssadapter")

	walledGarden := bss.WalledGardenConfig{
		Parameter:    os.Getenv("ACS_WALLED_GARDEN_PARAMETER"),
		SuspendValue: os.Getenv("ACS_WALLED_GARDEN_SUSPEND_VALUE"),
		ActiveValue:  os.Getenv("ACS_WALLED_GARDEN_ACTIVE_VALUE"),
	}
	if walledGarden.Parameter == "" {
		logger.Warn("ACS_WALLED_GARDEN_PARAMETER not set — SUSPEND/ACTIVATE orders will be rejected (build plan §5.3: no universal safe parameter across CPE vendors, so this isn't guessed at).")
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
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", metrics.Handler().ServeHTTP)
	mux.HandleFunc("POST /bss/v1/oauth/token", metrics.InstrumentHTTP("POST /bss/v1/oauth/token", h.issueOAuthToken))
	mux.HandleFunc("POST /bss/v1/mappings", metrics.InstrumentHTTP("POST /bss/v1/mappings", h.createMapping))
	mux.HandleFunc("GET /bss/v1/mappings/{account_id}", metrics.InstrumentHTTP("GET /bss/v1/mappings/{account_id}", h.listMappings))
	mux.HandleFunc("POST /bss/v1/orders", metrics.InstrumentHTTP("POST /bss/v1/orders", h.createOrder))
	mux.HandleFunc("GET /bss/v1/jobs/{command_key}", metrics.InstrumentHTTP("GET /bss/v1/jobs/{command_key}", h.getJob))
	mux.HandleFunc("POST /bss/v1/webhooks", metrics.InstrumentHTTP("POST /bss/v1/webhooks", h.createWebhookSubscription))
	mux.HandleFunc("GET /bss/v1/webhooks", metrics.InstrumentHTTP("GET /bss/v1/webhooks", h.listWebhookSubscriptions))
	mux.HandleFunc("DELETE /bss/v1/webhooks/{id}", metrics.InstrumentHTTP("DELETE /bss/v1/webhooks/{id}", h.deleteWebhookSubscription))

	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	go h.runWebhookNotifyLoop(workerCtx)
	go h.runWebhookDeliverLoop(workerCtx)

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
		Handler:           withAuth(token, oauthSigningSecret, withRateLimit(limiter, metrics, withMaxBody(mux))),
		ReadHeaderTimeout: 10 * time.Second,
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
	var serveErr error
	if certFile != "" && keyFile != "" {
		serveErr = server.ListenAndServeTLS(certFile, keyFile)
	} else {
		if server.TLSConfig != nil {
			logger.Error("ACS_BSS_MTLS_CA_CERT is set but ACS_BSS_TLS_CERT/ACS_BSS_TLS_KEY are not — mTLS needs the server to actually be running TLS")
			os.Exit(1)
		}
		serveErr = server.ListenAndServe()
	}
	if serveErr != nil {
		logger.Error("server error", "err", serveErr)
		os.Exit(1)
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
func withAuth(token string, oauthSigningSecret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (token == "" && len(oauthSigningSecret) == 0) ||
			(r.Method == http.MethodGet && r.URL.Path == "/metrics") ||
			(r.Method == http.MethodPost && r.URL.Path == "/bss/v1/oauth/token") {
			next.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("Authorization")

		if len(oauthSigningSecret) > 0 {
			if bearer, ok := strings.CutPrefix(got, "Bearer "); ok {
				if claims, err := auth.VerifyJWT(oauthSigningSecret, bearer); err == nil && claims.Role == bssClientRole {
					next.ServeHTTP(w, r)
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
}

type mappingResponse struct {
	AccountID   string `json:"account_id"`
	DeviceUUID  string `json:"device_uuid"`
	OUISerial   string `json:"oui_serial"`
	ServicePlan string `json:"service_plan,omitempty"`
	Status      string `json:"status"`
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

	mapping, err := h.mappings.CreateMapping(r.Context(), req.AccountID, req.OUISerial, req.ServicePlan)
	if errors.Is(err, bss.ErrDeviceNotFound) {
		writeError(w, http.StatusNotFound, "ErrDeviceNotMapped", err.Error())
		return
	}
	if err != nil {
		h.logger.Error("failed to create mapping", "err", err, "account_id", req.AccountID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	if req.DeviceUUID != "" && req.DeviceUUID != mapping.DeviceID {
		writeError(w, http.StatusBadRequest, "ErrInvalidRequest", "device_uuid does not match the device resolved from oui_serial")
		return
	}

	if err := h.auditor.Record(r.Context(), "bss:"+req.AccountID, mapping.DeviceID, "BSSMappingCreated", map[string]any{
		"account_id": req.AccountID, "oui_serial": req.OUISerial, "service_plan": req.ServicePlan,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("mapping created", "account_id", req.AccountID, "device_id", mapping.DeviceID)

	writeJSON(w, http.StatusOK, mappingResponse{
		AccountID: mapping.AccountID, DeviceUUID: mapping.DeviceID, OUISerial: mapping.OUISerial,
		ServicePlan: mapping.ServicePlan, Status: mapping.Status,
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
			ServicePlan: m.ServicePlan, Status: m.Status,
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
	Parameters      map[string]string `json:"parameters"`
}

type orderResponse struct {
	OrderTrackingID string    `json:"order_tracking_id"`
	CommandKey      string    `json:"command_key"`
	Status          string    `json:"status"`
	Timestamp       time.Time `json:"timestamp"`
}

// createOrder implements Workflow B, idempotently: a retried
// external_order_id is answered from bss_orders (with the job's *current*
// status, not a stale "QUEUED") instead of dispatching a second job — the
// gap build plan §5.3 flagged in the reference draft.
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
		status, err := h.acs.GetJobStatus(r.Context(), existing.CommandKey)
		if err != nil {
			h.logger.Error("failed to fetch status for existing order", "err", err, "command_key", existing.CommandKey)
			writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
			return
		}
		writeJSON(w, http.StatusAccepted, orderResponse{
			OrderTrackingID: req.ExternalOrderID, CommandKey: existing.CommandKey,
			Status: status.Status, Timestamp: time.Now().UTC(),
		})
		return
	}

	mapping, err := h.mappings.PrimaryDeviceForAccount(r.Context(), req.AccountID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "ErrDeviceNotMapped", "no active device is mapped to this account")
		return
	}
	if err != nil {
		h.logger.Error("failed to resolve account mapping", "err", err, "account_id", req.AccountID)
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

	commandKey, err := h.acs.SetParameters(r.Context(), mapping.DeviceID, params)
	if errors.Is(err, bss.ErrACSUnreachable) {
		writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
		return
	}
	if err != nil {
		h.logger.Error("failed to dispatch order to ACS", "err", err, "account_id", req.AccountID, "action", req.Action)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}

	// Best-effort: the job is already queued on the ACS side at this
	// point. If recording the idempotency row fails, a retried
	// external_order_id will not find it and will dispatch a second job —
	// true exactly-once here would need an outbox/distributed-transaction
	// pattern, which is out of scope for Phase 8b. Logged loudly so it's
	// visible in practice rather than silently accepted.
	if err := h.mappings.RecordOrder(r.Context(), bss.OrderRecord{
		ExternalOrderID: req.ExternalOrderID, AccountID: req.AccountID, Action: req.Action, CommandKey: commandKey,
	}); err != nil {
		h.logger.Error("failed to record order idempotency row — a retry of this external_order_id WILL double-dispatch",
			"err", err, "external_order_id", req.ExternalOrderID, "command_key", commandKey)
	}

	if err := h.auditor.Record(r.Context(), "bss:"+req.AccountID, mapping.DeviceID, "BSSOrderDispatched", map[string]any{
		"external_order_id": req.ExternalOrderID, "action": req.Action, "command_key": commandKey,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("order dispatched", "external_order_id", req.ExternalOrderID, "account_id", req.AccountID,
		"action", req.Action, "command_key", commandKey)

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
