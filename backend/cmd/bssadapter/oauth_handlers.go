// OAuth2 client-credentials token endpoint (RFC 6749 §4.4) — the
// production-grade replacement for the shared static bearer token. See
// internal/bss/oauth.go's doc comment for the full rationale.
package main

import (
	"encoding/json"
	"net/http"
	"time"

	"acs/internal/auth"
)

const oauthTokenTTL = time.Hour

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// issueOAuthToken implements the client_credentials grant. Per RFC 6749
// §2.3.1, the client authenticates via HTTP Basic auth (preferred) or
// client_id/client_secret form fields (accepted too — some BSS/CRM OAuth2
// client libraries only support the body form). This endpoint itself is
// exempt from the bearer-token check every other /bss/v1 route requires
// — it has its own credential check right here.
func (h *handler) issueOAuthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not parse request body")
		return
	}
	if got := r.FormValue("grant_type"); got != "client_credentials" {
		writeError(w, http.StatusBadRequest, "unsupported_grant_type", `only "client_credentials" is supported`)
		return
	}

	clientID, clientSecret, ok := r.BasicAuth()
	if !ok {
		clientID, clientSecret = r.FormValue("client_id"), r.FormValue("client_secret")
	}
	if clientID == "" || clientSecret == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "client_id and client_secret are required (Basic auth or form fields)")
		return
	}

	if err := h.oauthClients.VerifyCredentials(r.Context(), clientID, clientSecret); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}

	now := time.Now()
	claims := auth.Claims{Subject: "bss-client:" + clientID, Role: bssClientRole, IssuedAt: now, ExpiresAt: now.Add(oauthTokenTTL)}
	token, err := auth.SignJWT(h.oauthSigningSecret, claims)
	if err != nil {
		h.logger.Error("failed to sign oauth token", "err", err, "client_id", clientID)
		writeError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}

	h.logger.Info("oauth token issued", "client_id", clientID)
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: token, TokenType: "Bearer", ExpiresIn: int(oauthTokenTTL.Seconds())})
}
