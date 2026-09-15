// OAuth2 client-credentials token endpoint (RFC 6749 §4.4) — the
// production-grade replacement for the shared static bearer token. See
// internal/bss/oauth.go's doc comment for the full rationale.
package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"acs/internal/auth"
)

const oauthTokenTTL = time.Hour

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
}

// issueOAuthToken implements the client_credentials grant. The stored
// policy is copied into the short-lived JWT. A requested OAuth scope may
// narrow that policy but can never add a permission the client was not
// registered for.
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

	client, err := h.oauthClients.AuthenticateClient(r.Context(), clientID, clientSecret)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}

	scopes := append([]string(nil), client.Scopes...)
	if requested := strings.Fields(r.FormValue("scope")); len(requested) > 0 {
		allowed := make(map[string]struct{}, len(client.Scopes))
		for _, scope := range client.Scopes {
			allowed[scope] = struct{}{}
		}
		for _, scope := range requested {
			if _, ok := allowed[scope]; !ok {
				writeError(w, http.StatusBadRequest, "invalid_scope", "requested scope is not granted to this client")
				return
			}
		}
		scopes = requested
	}

	now := time.Now()
	claims := auth.Claims{
		Subject: "bss-client:" + clientID, Role: bssClientRole, IssuedAt: now, ExpiresAt: now.Add(oauthTokenTTL),
		Scopes: scopes, AccountIDs: append([]string(nil), client.AccountIDs...), GlobalAccess: client.GlobalAccess,
	}
	token, err := auth.SignJWT(h.oauthSigningSecret, claims)
	if err != nil {
		h.logger.Error("failed to sign oauth token", "err", err, "client_id", clientID)
		writeError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}

	h.logger.Info("oauth token issued", "client_id", clientID, "scopes", scopes, "global_access", client.GlobalAccess)
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: token, TokenType: "Bearer", ExpiresIn: int(oauthTokenTTL.Seconds()), Scope: strings.Join(scopes, " ")})
}
