// Operator authentication (build plan §4 Phase 6 / design doc v3 §11.3
// credential class 4). There is no external identity provider in this
// lab, so cmd/api is its own minimal issuer: operators are rows in
// Postgres (bcrypt-hashed passwords), login exchanges credentials for a
// short-lived JWT, and a middleware enforces it — but only when
// ACS_JWT_SIGNING_SECRET is set. Unset, this runs exactly like Phases
// 1-5 did: open, with a loud startup warning, the same "Enabled()"
// convention internal/auth.DigestAuthenticator and cmd/bssadapter's
// bearer token already use. That keeps the existing frontend (built
// against an unauthenticated API) working by default while making real
// enforcement one env var away.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"acs/internal/auth"
	"acs/internal/observability"
	"acs/internal/operators"
	"acs/internal/ratelimit"
	"acs/internal/tenancy"
	"golang.org/x/crypto/bcrypt"
)

const jwtTTL = 8 * time.Hour

// browserTicketTTL bounds a ticket minted for a WebSocket/iframe
// handshake (audit P1.4): long enough to open the connection, far too
// short to be useful if it leaks through a log, referrer, or history.
const browserTicketTTL = 60 * time.Second

// Login brute-force control (audit "missing checks"): a token bucket per
// normalized username + client IP, independent of the general API rate
// limit, so a password-guessing loop against one account is throttled
// long before the per-IP bucket notices — five attempts, then one every
// twelve seconds. Keyed on both so an attacker cannot lock out a real
// user from elsewhere, nor spray usernames from one address freely.
var loginLimiter = ratelimit.New(1.0/12, 5, 30*time.Minute)

// tokenVersionCacheTTL bounds how long a revoked JWT keeps working: the
// per-request revocation check hits Postgres at most once per operator
// per TTL instead of on every call.
const tokenVersionCacheTTL = 15 * time.Second

type versionEntry struct {
	version int
	expires time.Time
}

// tokenCurrent reports whether claims' Version is still the operator's
// live token_version (see operators.RevokeSessions). Fails closed on a
// lookup error — a DB blip must not resurrect revoked sessions.
func (h *handler) tokenCurrent(ctx context.Context, claims *auth.Claims) bool {
	if claims.Subject == serviceSubject {
		return true
	}
	h.versionMu.Lock()
	entry, ok := h.versionCache[claims.Subject]
	h.versionMu.Unlock()
	if !ok || time.Now().After(entry.expires) {
		v, err := h.operators.TokenVersion(ctx, claims.Subject)
		if err != nil {
			h.logger.Error("token revocation check failed", "err", err, "username", claims.Subject)
			return false
		}
		entry = versionEntry{version: v, expires: time.Now().Add(tokenVersionCacheTTL)}
		h.versionMu.Lock()
		if h.versionCache == nil {
			h.versionCache = map[string]versionEntry{}
		}
		h.versionCache[claims.Subject] = entry
		h.versionMu.Unlock()
	}
	return claims.Version >= entry.version
}

// forgetTokenVersion drops the cache entry so a revocation performed by
// this process takes effect immediately for it.
func (h *handler) forgetTokenVersion(username string) {
	h.versionMu.Lock()
	delete(h.versionCache, username)
	h.versionMu.Unlock()
}

type ctxKey int

const claimsCtxKey ctxKey = 0

func withOperatorClaims(ctx context.Context, claims *auth.Claims) context.Context {
	return context.WithValue(ctx, claimsCtxKey, claims)
}

func operatorClaims(ctx context.Context) (*auth.Claims, bool) {
	claims, ok := ctx.Value(claimsCtxKey).(*auth.Claims)
	return claims, ok && claims != nil
}

// serviceSubject is the synthetic operator identity assigned to a request
// authenticated via the internal service token rather than a real
// operator JWT — shows up in audit records exactly like a normal
// operator's username would.
const serviceSubject = "service:bssadapter"

// withJWTAuth enforces a Bearer JWT on every request except login itself
// and the firmware file-serve route — that one is fetched by CPEs via
// their Download RPC URL, not by an operator, and has no JWT to send.
//
// It also accepts one alternate credential: internalServiceToken, a
// shared bearer secret for cmd/bssadapter's own machine-to-machine calls
// into this API (internal/bss/acsclient.go's SetParameters/GetJobStatus).
// bssadapter is a process, not an operator — it has no username/password
// to log in with — so without this, every BSS order dispatch fails with
// 401 the moment operator JWT auth is actually turned on, which defeats
// the entire point of Workflow B. Same shape as cmd/bssadapter's own
// withAuth (crypto/subtle.ConstantTimeCompare against one shared secret,
// not a real login), applied one process boundary further in.
// current, when non-nil, is the revocation check applied to every
// verified operator JWT (handler.tokenCurrent); nil skips it (unit tests
// of the placement rules alone).
func withJWTAuth(secret []byte, internalServiceToken string, current func(context.Context, *auth.Claims) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(secret) == 0 || isPublicRoute(r) {
			next.ServeHTTP(w, r)
			return
		}

		token, fromQuery, ok := bearerToken(r)
		if !ok {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		// A credential in the URL is accepted on exactly the two routes a
		// browser cannot send a header to (audit P1.4), and then only a
		// purpose-bound ticket — never a session JWT or the service token.
		if fromQuery && !isBrowserTicketRoute(r) {
			http.Error(w, "credentials in the query string are not accepted on this route", http.StatusUnauthorized)
			return
		}

		if !fromQuery && internalServiceToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(internalServiceToken)) == 1 {
			// The machine identity is deliberately narrow (audit P1.4): it
			// may do only what internal/bss/acsclient.go does.
			if !isServiceRoute(r) {
				http.Error(w, "internal service token is not permitted on this route", http.StatusForbidden)
				return
			}
			claims := &auth.Claims{Subject: serviceSubject, Role: operators.RoleSuperAdmin, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
			next.ServeHTTP(w, r.WithContext(withOperatorClaims(r.Context(), claims)))
			return
		}

		claims, err := auth.VerifyJWT(secret, token)
		if err != nil {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
		// Token kind must match how it arrived: tickets only in the query
		// string on ticket routes, session tokens only in the header.
		if fromQuery != (claims.Audience == auth.AudienceBrowserTicket) {
			http.Error(w, "wrong token type for this route", http.StatusUnauthorized)
			return
		}
		if current != nil && !current(r.Context(), claims) {
			http.Error(w, "session revoked — sign in again", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r.WithContext(withOperatorClaims(r.Context(), claims)))
	})
}

// isBrowserTicketRoute names the routes whose handshake a browser makes
// without control over request headers: the console WebSocket and the
// web-GUI iframe. These accept a browser ticket in ?token= and nothing
// else in the query string.
func isBrowserTicketRoute(r *http.Request) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/v1/devices/") {
		return false
	}
	return strings.HasSuffix(p, "/cli/connect") || strings.Contains(p, "/webgui/proxy/")
}

// isServiceRoute is the allowlist for the internal service token — the
// exact calls internal/bss/acsclient.go makes on behalf of
// cmd/bssadapter, nothing else (no operator management, no tenancy, no
// firmware, no console).
func isServiceRoute(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/api/v1/devices/") && strings.HasSuffix(p, "/parameters"):
		return true
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/devices/") && strings.Count(p, "/") == 4:
		return true // GET /api/v1/devices/{id}
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v1/jobs/"):
		return true
	}
	return false
}

func isPublicRoute(r *http.Request) bool {
	if r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login" {
		return true
	}
	if r.Method == http.MethodPost && (r.URL.Path == "/api/v1/auth/password-reset/request" || r.URL.Path == "/api/v1/auth/password-reset/confirm") {
		// Self-service password reset has to be reachable by a locked-out
		// operator who has no JWT at all — same reasoning as /auth/login.
		// Safety is the token itself (random 32-byte, 4-hour TTL,
		// single-use — internal/operators.CreateResetToken/ConsumeResetToken),
		// not a bearer credential.
		return true
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/firmware/images/") && strings.HasSuffix(r.URL.Path, "/file") {
		return true
	}
	if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/uploads/") && strings.HasSuffix(r.URL.Path, "/receive") {
		// Upload RPC's receipt endpoint — the CPE PUTs the file here
		// directly, with no operator JWT to present, same reasoning as the
		// firmware file-serve route above but in the opposite direction.
		return true
	}
	if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
		return true // liveness/readiness probes carry no data — see observability.LivenessHandler
	}
	if r.Method == http.MethodGet && r.URL.Path == "/metrics" {
		// A Prometheus scraper has no operator JWT to present (build plan
		// §4 Phase 7).
		return true
	}
	return false
}

// withRateLimit enforces a per-operator token bucket (build plan §7.3/§7d
// — deliberately shipped after JWT auth, not before: an unauthenticated
// limiter here could only key on IP, which is weak for an internal API).
// Runs inside withJWTAuth, so operatorClaims is already in context when
// auth is enabled; falls back to remote address when it's disabled (lab
// mode) or for the always-public /metrics and /auth/login routes, which
// have no operator identity to key on.
func withRateLimit(limiter *ratelimit.Limiter, metrics *observability.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		key := r.RemoteAddr
		if claims, ok := operatorClaims(r.Context()); ok {
			key = claims.Subject
		}
		if !limiter.Allow(key) {
			metrics.RateLimitRejectedTotal.Inc()
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken reads the credential from the Authorization header, or —
// reporting fromQuery=true so the caller can apply the stricter rules —
// from a ?token= query parameter. The query form exists only because the
// browser WebSocket API and an <iframe src> cannot set request headers;
// withJWTAuth confines it to those two routes and to purpose-bound
// tickets (audit P1.4).
func bearerToken(r *http.Request) (token string, fromQuery bool, ok bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && h[:len(prefix)] == prefix {
		return h[len(prefix):], false, true
	}
	if t := r.URL.Query().Get("token"); t != "" {
		return t, true, true
	}
	return "", false, false
}

// issueBrowserTicket mints a short-lived, audience-bound ticket for the
// calling operator (audit P1.4). The frontend requests one immediately
// before opening the console WebSocket or the web-GUI iframe and puts
// it — not the session JWT — in that URL's ?token=. It cannot be used
// in an Authorization header, on any other route, or after 60 seconds.
func (h *handler) issueBrowserTicket(w http.ResponseWriter, r *http.Request) {
	claims, ok := operatorClaims(r.Context())
	if !ok {
		http.Error(w, "no authenticated operator", http.StatusUnauthorized)
		return
	}
	if claims.Subject == serviceSubject {
		http.Error(w, "service identities cannot mint browser tickets", http.StatusForbidden)
		return
	}
	now := time.Now().UTC()
	// audit P1.5: a ticket carries the operator's token version from the
	// parent session JWT that authenticated this request, not the zero
	// value — withJWTAuth's version check (line ~170) applies to ticket
	// routes exactly like any other, so a ticket minted with no version
	// would either outlive a revocation it should have died with (an
	// operator whose TokenVersion has never left its initial value), or
	// come out dead on arrival forever after the first revocation (any
	// operator whose TokenVersion has since moved past that initial
	// value) — this ties a ticket's validity to the same revocation
	// event as the session it was minted from, and to no other.
	ticket, err := auth.SignJWT(h.jwtSecret, auth.Claims{
		Subject: claims.Subject, Role: claims.Role, Audience: auth.AudienceBrowserTicket,
		IssuedAt: now, ExpiresAt: now.Add(browserTicketTTL), Version: claims.Version,
	})
	if err != nil {
		h.logger.Error("failed to sign browser ticket", "err", err, "username", claims.Subject)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ticket": ticket, "expires_in": int(browserTicketTTL.Seconds())})
}

// requireRole wraps a handler with a minimum-role check. When auth is
// disabled (h.jwtSecret unset), every route is open — matching every
// other credential gate in this codebase's "off unless configured" shape.
func (h *handler) requireRole(min string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(h.jwtSecret) == 0 {
			next(w, r)
			return
		}
		claims, ok := operatorClaims(r.Context())
		if !ok || !operators.AtLeast(claims.Role, min) {
			http.Error(w, "insufficient role", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// requirePermission wraps a handler with a curated-capability check
// (migration 0032's role_permissions — see internal/operators' permission
// catalog for which ~13 capabilities this covers and why not all 72
// routes). readonly can never satisfy this (it has no permissions by
// definition, checked via operators.PermissionRepository.Has), noc/manager
// depend on the configurable matrix, superadmin always passes. Same
// "auth disabled -> open" behavior as requireRole when h.jwtSecret is unset.
func (h *handler) requirePermission(perm string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(h.jwtSecret) == 0 {
			next(w, r)
			return
		}
		claims, ok := operatorClaims(r.Context())
		if !ok {
			http.Error(w, "insufficient role", http.StatusForbidden)
			return
		}
		granted, err := h.permissions.Has(r.Context(), claims.Role, perm)
		if err != nil {
			h.logger.Error("failed to check permission", "err", err, "role", claims.Role, "permission", perm)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !granted {
			http.Error(w, "insufficient permission", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// --- login -------------------------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string `json:"token"`
	Role      string `json:"role"`
	ExpiresAt string `json:"expires_at"`
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	if len(h.jwtSecret) == 0 {
		http.Error(w, "operator auth is not configured on this server", http.StatusServiceUnavailable)
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Password == "" {
		http.Error(w, "username and password are required", http.StatusBadRequest)
		return
	}
	if !loginLimiter.Allow(strings.ToLower(strings.TrimSpace(req.Username)) + "|" + clientIP(r)) {
		h.metrics.RateLimitRejectedTotal.Inc()
		http.Error(w, "too many login attempts — try again shortly", http.StatusTooManyRequests)
		return
	}

	op, err := h.operators.ByUsername(r.Context(), req.Username)
	if err != nil {
		// Same response whether the username doesn't exist or the
		// password is wrong — a distinct "no such user" response would
		// let a caller enumerate valid usernames.
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(op.PasswordHash), []byte(req.Password)) != nil {
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	// Checked after the password comparison, and answered with the same
	// message, so this cannot be used to probe which accounts exist or
	// which have been disabled. The bcrypt compare above also keeps the
	// timing of a disabled account indistinguishable from an active one.
	if op.DisabledAt != nil {
		h.logger.Warn("login attempt against a disabled operator", "username", op.Username)
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}

	now := time.Now().UTC()
	claims := auth.Claims{Subject: op.Username, Role: op.Role, IssuedAt: now, ExpiresAt: now.Add(jwtTTL), Version: op.TokenVersion}
	token, err := auth.SignJWT(h.jwtSecret, claims)
	if err != nil {
		h.logger.Error("failed to sign JWT", "err", err, "username", op.Username)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.auditor.Record(r.Context(), op.Username, "", "OperatorLogin", map[string]any{"role": op.Role}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("operator logged in", "username", op.Username, "role", op.Role)

	writeJSON(w, http.StatusOK, loginResponse{Token: token, Role: op.Role, ExpiresAt: claims.ExpiresAt.Format(time.RFC3339)})
}

// logout revokes every session of the calling operator (audit "missing
// checks": JWT revocation) by bumping their token_version — the current
// token and any other copies of it stop verifying within
// tokenVersionCacheTTL on other replicas, immediately on this one.
func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	claims, ok := operatorClaims(r.Context())
	if !ok || claims.Subject == serviceSubject {
		http.Error(w, "no operator session to revoke", http.StatusBadRequest)
		return
	}
	op, err := h.operators.ByUsername(r.Context(), claims.Subject)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.operators.RevokeSessions(r.Context(), op.ID); err != nil {
		h.logger.Error("failed to revoke sessions", "err", err, "username", op.Username)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.forgetTokenVersion(op.Username)
	if err := h.auditor.Record(r.Context(), op.Username, "", "OperatorLogout", nil); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// clientIP is the peer address without port. X-Forwarded-For is
// deliberately not consulted: this API is meant to sit behind a proxy
// the operator controls, but trusting the header unconditionally would
// let any client choose its own rate-limit key.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- operator management (admin-only) ----------------------------------

type createOperatorRequest struct {
	Username string `json:"username"`
	Email    string `json:"email,omitempty"` // optional — required only if this operator will use self-service email password reset
	Password string `json:"password"`
	Role     string `json:"role"`
	// Scopes and GlobalAccess are optional initial tenancy grants (audit
	// P0.1) applied right after the operator row is created. Until
	// either is set, a non-superadmin operator has zero device access —
	// the safe direction for the gap between the two writes, since it
	// only ever under-grants, never over-grants.
	Scopes       []scopeDTO `json:"scopes,omitempty"`
	GlobalAccess bool       `json:"global_access,omitempty"`
}

type operatorResponse struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email,omitempty"`
	Role         string `json:"role"`
	GlobalAccess bool   `json:"global_access"`
	CreatedAt    string `json:"created_at"`
	// Null for an active operator; the offboarding timestamp otherwise
	// (audit 2026-09-04 P1.4).
	DisabledAt *string `json:"disabled_at"`
}

func toOperatorResponse(op operators.Operator) operatorResponse {
	var disabledAt *string
	if op.DisabledAt != nil {
		s := op.DisabledAt.Format(time.RFC3339)
		disabledAt = &s
	}
	return operatorResponse{ID: op.ID, Username: op.Username, Email: op.Email, Role: op.Role, GlobalAccess: op.GlobalAccess, CreatedAt: op.CreatedAt.Format(time.RFC3339), DisabledAt: disabledAt}
}

func (h *handler) createOperator(w http.ResponseWriter, r *http.Request) {
	var req createOperatorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Password == "" || !operators.ValidRole(req.Role) {
		http.Error(w, "username, password, and a valid role (superadmin, manager, noc, readonly) are required", http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		h.logger.Error("failed to hash password", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	op, err := h.operators.Create(r.Context(), req.Username, req.Email, string(hash), req.Role)
	if errors.Is(err, operators.ErrUsernameTaken) {
		http.Error(w, "username already exists", http.StatusConflict)
		return
	}
	if err != nil {
		h.logger.Error("failed to create operator", "err", err, "username", req.Username)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Optional initial grants (audit P0.1): applied after the operator row
	// exists, not in the same transaction, but the gap is safe in only one
	// direction — until these land, the new operator has zero access, never
	// more than intended.
	if req.GlobalAccess {
		if err := h.operators.SetGlobalAccess(r.Context(), op.ID, true); err != nil {
			h.logger.Error("failed to set initial global access", "err", err, "operator_id", op.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		op.GlobalAccess = true
	}
	if len(req.Scopes) > 0 {
		scopes := make([]tenancy.Scope, len(req.Scopes))
		for i, s := range req.Scopes {
			if s.Type != tenancy.ScopeRegion && s.Type != tenancy.ScopeCustomer {
				http.Error(w, `scope type must be "region" or "customer"`, http.StatusBadRequest)
				return
			}
			scopes[i] = tenancy.Scope{Type: s.Type, ID: s.ID}
		}
		if err := h.tenancy.SetOperatorScopes(r.Context(), op.ID, scopes); err != nil {
			h.logger.Error("failed to set initial operator scopes", "err", err, "operator_id", op.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "OperatorCreated", map[string]any{
		"username": op.Username, "role": op.Role,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("operator created", "username", op.Username, "role", op.Role, "created_by", actor)

	writeJSON(w, http.StatusCreated, toOperatorResponse(*op))
}

// listOperators is admin-only (build plan gap: "no operator-management UI"
// — creating operators had a REST endpoint since Phase 6, but nothing to
// review who already exists). Password hashes never leave this handler.
func (h *handler) listOperators(w http.ResponseWriter, r *http.Request) {
	ops, err := h.operators.List(r.Context())
	if err != nil {
		h.logger.Error("failed to list operators", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]operatorResponse, 0, len(ops))
	for _, op := range ops {
		out = append(out, toOperatorResponse(op))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// bootstrapAdmin creates the very first operator from env vars if the
// operators table is empty — otherwise there is no way to create the
// first superadmin at all, since creating one requires already being one.
func bootstrapAdmin(ctx context.Context, h *handler, username, password string) error {
	if username == "" || password == "" {
		return nil
	}
	n, err := h.operators.Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = h.operators.Create(ctx, username, "", string(hash), operators.RoleSuperAdmin)
	return err
}
