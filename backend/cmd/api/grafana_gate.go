package main

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"acs/internal/auth"
	"acs/internal/operators"
)

// The Grafana gate lets a signed-in console operator reach Grafana on the
// public address without exposing Grafana to everyone (ACS_GRAFANA_GATE=1).
//
//  1. The console asks POST /api/v1/grafana/ticket for a 30-second ticket
//     and opens <ACS_GRAFANA_URL>/acs-gate/enter?ticket=… in a new tab.
//  2. The reverse proxy serving Grafana's origin forwards /acs-gate/enter
//     to GET /api/v1/grafana/enter, which checks the ticket, sets an
//     HttpOnly session cookie on that origin and redirects to Grafana.
//  3. The proxy authorises every other Grafana request with an
//     auth_request to GET /api/v1/grafana/check, which checks the cookie.
//     Grafana's own login still follows. A ticket opens Grafana once.
//
// Grafana keeps its own origin (another port or host name): served under
// the console's origin, a script inside Grafana could read the console's
// session token from browser storage. Its dashboards read every
// customer's data, so only platform operators get through: a role with
// monitoring.view and no customer scope (or a superadmin). Each check
// re-verifies the operator's token version, so a logout, a password
// change or disabling the operator ends Grafana access at once.
// infra/ingress/ has the proxy configuration.

const (
	grafanaTicketTTL  = 30 * time.Second
	grafanaSessionTTL = time.Hour
	grafanaGateCookie = "acs_grafana_gate"
)

// grafanaTickets remembers spent tickets until they expire, so a ticket
// opens Grafana once even if its URL is seen in its 30 seconds. It is per
// cmd/api process: with several API replicas behind the proxy, route
// /acs-gate/enter to one of them.
var grafanaTickets = struct {
	sync.Mutex
	spent map[[32]byte]time.Time
}{spent: map[[32]byte]time.Time{}}

// spendGrafanaTicket reports whether ticket was unspent, and spends it.
func spendGrafanaTicket(ticket string, expires time.Time) bool {
	key := sha256.Sum256([]byte(ticket))
	now := time.Now()
	grafanaTickets.Lock()
	defer grafanaTickets.Unlock()
	for k, exp := range grafanaTickets.spent {
		if now.After(exp) {
			delete(grafanaTickets.spent, k)
		}
	}
	if _, spent := grafanaTickets.spent[key]; spent {
		return false
	}
	grafanaTickets.spent[key] = expires
	return true
}

// grafanaAllowed reports whether an operator may pass the gate.
func (h *handler) grafanaAllowed(ctx context.Context, claims *auth.Claims) (bool, error) {
	if claims.Subject == "" || claims.Subject == serviceSubject {
		return false, nil
	}
	if claims.Role == operators.RoleSuperAdmin {
		return true, nil
	}
	granted, err := h.permissions.Has(ctx, claims.Role, operators.PermMonitoringView)
	if err != nil || !granted {
		return false, err
	}
	op, err := h.operators.ByUsername(ctx, claims.Subject)
	if err != nil {
		return false, err
	}
	if op.GlobalAccess {
		return true, nil
	}
	_, scoped, err := h.tenancy.AccessibleCustomerIDs(ctx, op.ID)
	return err == nil && !scoped, err
}

// issueGrafanaTicket is POST /api/v1/grafana/ticket.
func (h *handler) issueGrafanaTicket(w http.ResponseWriter, r *http.Request) {
	if !h.grafanaGate || h.grafanaURL == "" {
		http.Error(w, "the Grafana gate is not configured (ACS_GRAFANA_GATE=1 and ACS_GRAFANA_URL)", http.StatusNotFound)
		return
	}
	if len(h.jwtSecret) == 0 {
		http.Error(w, "the Grafana gate needs operator sign-in (ACS_JWT_SIGNING_SECRET)", http.StatusServiceUnavailable)
		return
	}
	claims, ok := operatorClaims(r.Context())
	if !ok {
		http.Error(w, "no authenticated operator", http.StatusUnauthorized)
		return
	}
	allowed, err := h.grafanaAllowed(r.Context(), claims)
	if err != nil {
		h.logger.Error("failed to check Grafana access", "err", err, "username", claims.Subject)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "Grafana is limited to platform operators: monitoring.view and no customer scope, because its dashboards span every customer", http.StatusForbidden)
		return
	}
	now := time.Now()
	ticket, err := auth.SignJWT(h.jwtSecret, auth.Claims{
		Subject: claims.Subject, Role: claims.Role, Audience: auth.AudienceGrafanaTicket,
		IssuedAt: now, ExpiresAt: now.Add(grafanaTicketTTL), Version: claims.Version,
	})
	if err != nil {
		h.logger.Error("failed to sign Grafana ticket", "err", err, "username", claims.Subject)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"url":        strings.TrimRight(h.grafanaURL, "/") + "/acs-gate/enter?ticket=" + url.QueryEscape(ticket),
		"expires_in": int(grafanaTicketTTL.Seconds()),
	})
}

// grafanaClaims verifies a gate token of the given audience and that its
// operator may still pass.
func (h *handler) grafanaClaims(ctx context.Context, token, audience string) (*auth.Claims, bool) {
	if token == "" || len(h.jwtSecret) == 0 {
		return nil, false
	}
	claims, err := auth.VerifyJWT(h.jwtSecret, token)
	if err != nil || claims.Audience != audience || !h.tokenCurrent(ctx, claims) {
		return nil, false
	}
	allowed, err := h.grafanaAllowed(ctx, claims)
	if err != nil {
		h.logger.Error("failed to check Grafana access", "err", err, "username", claims.Subject)
	}
	return claims, allowed
}

// grafanaEnter is GET /api/v1/grafana/enter?ticket=…, reached through the
// proxy at Grafana's origin: the ticket becomes a session cookie there.
func (h *handler) grafanaEnter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !h.grafanaGate {
		http.NotFound(w, r)
		return
	}
	ticket := r.URL.Query().Get("ticket")
	claims, ok := h.grafanaClaims(r.Context(), ticket, auth.AudienceGrafanaTicket)
	if ok && !spendGrafanaTicket(ticket, claims.ExpiresAt) {
		ok = false
	}
	if !ok {
		http.Error(w, "This Grafana link has expired or is not valid. Open Grafana again from the ACS console.", http.StatusUnauthorized)
		return
	}
	now := time.Now()
	session, err := auth.SignJWT(h.jwtSecret, auth.Claims{
		Subject: claims.Subject, Role: claims.Role, Audience: auth.AudienceGrafanaSession,
		IssuedAt: now, ExpiresAt: now.Add(grafanaSessionTTL), Version: claims.Version,
	})
	if err != nil {
		h.logger.Error("failed to sign Grafana session", "err", err, "username", claims.Subject)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: grafanaGateCookie, Value: session, Path: "/", MaxAge: int(grafanaSessionTTL.Seconds()),
		HttpOnly: true, Secure: strings.HasPrefix(h.grafanaURL, "https://"), SameSite: http.SameSiteLaxMode,
	})
	if err := h.auditor.Record(r.Context(), claims.Subject, "", "GrafanaAccessGranted", map[string]any{
		"expires_at": now.Add(grafanaSessionTTL).UTC(),
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// grafanaCheck is GET /api/v1/grafana/check, the proxy's auth_request for
// every Grafana request: 204 lets it through, 401 stops it.
func (h *handler) grafanaCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.grafanaGate {
		http.NotFound(w, r)
		return
	}
	cookie, err := r.Cookie(grafanaGateCookie)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if _, ok := h.grafanaClaims(r.Context(), cookie.Value, auth.AudienceGrafanaSession); !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
