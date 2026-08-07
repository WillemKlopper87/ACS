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
	"net/http"
	"strings"
	"time"

	"acs/internal/auth"
	"acs/internal/observability"
	"acs/internal/operators"
	"acs/internal/ratelimit"
	"golang.org/x/crypto/bcrypt"
)

const jwtTTL = 8 * time.Hour

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
func withJWTAuth(secret []byte, internalServiceToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(secret) == 0 || isPublicRoute(r) {
			next.ServeHTTP(w, r)
			return
		}

		token, ok := bearerToken(r)
		if !ok {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}

		if internalServiceToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(internalServiceToken)) == 1 {
			claims := &auth.Claims{Subject: serviceSubject, Role: operators.RoleSuperAdmin, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
			next.ServeHTTP(w, r.WithContext(withOperatorClaims(r.Context(), claims)))
			return
		}

		claims, err := auth.VerifyJWT(secret, token)
		if err != nil {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r.WithContext(withOperatorClaims(r.Context(), claims)))
	})
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

// bearerToken reads the operator JWT from the Authorization header, or
// falls back to a ?token= query parameter — the browser WebSocket API
// (used by the device console's SSH/Telnet bridge) cannot set custom
// request headers on the handshake, so that's the only way a WS client can
// present its token at all. This is strictly additive (still requires the
// same valid signed JWT) and a common pattern for exactly this constraint.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && h[:len(prefix)] == prefix {
		return h[len(prefix):], true
	}
	if t := r.URL.Query().Get("token"); t != "" {
		return t, true
	}
	return "", false
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

	now := time.Now().UTC()
	claims := auth.Claims{Subject: op.Username, Role: op.Role, IssuedAt: now, ExpiresAt: now.Add(jwtTTL)}
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

// --- operator management (admin-only) ----------------------------------

type createOperatorRequest struct {
	Username string `json:"username"`
	Email    string `json:"email,omitempty"` // optional — required only if this operator will use self-service email password reset
	Password string `json:"password"`
	Role     string `json:"role"`
}

type operatorResponse struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email,omitempty"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

func toOperatorResponse(op operators.Operator) operatorResponse {
	return operatorResponse{ID: op.ID, Username: op.Username, Email: op.Email, Role: op.Role, CreatedAt: op.CreatedAt.Format(time.RFC3339)}
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
