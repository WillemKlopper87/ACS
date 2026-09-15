package main

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"acs/internal/auth"
	"acs/internal/bss"
)

// tmfPrincipal re-validates the already-authenticated bearer so individual
// TMF handlers can enforce the integration's persisted authorization policy.
// withAuth remains the authentication/revocation gate; this helper is the
// finer-grained northbound authorization gate.
func (h *handler) tmfPrincipal(w http.ResponseWriter, r *http.Request, requiredScope string) (*auth.Claims, bool) {
	got := r.Header.Get("Authorization")

	// The legacy shared token predates per-integration policy. Keep it as an
	// explicitly privileged compatibility path until deployments remove
	// ACS_BSS_API_TOKEN; startup already warns that it is deprecated.
	if h.token != "" {
		want := "Bearer " + h.token
		if len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
			return &auth.Claims{Role: bssClientRole, Scopes: bss.AllTMFScopes(), GlobalAccess: true}, true
		}
	}

	bearer, ok := strings.CutPrefix(got, "Bearer ")
	if !ok || len(h.oauthSigningSecret) == 0 {
		writeError(w, http.StatusUnauthorized, "invalid_token", "authentication required")
		return nil, false
	}
	claims, err := auth.VerifyJWT(h.oauthSigningSecret, bearer)
	if err != nil || claims.Role != bssClientRole {
		writeError(w, http.StatusUnauthorized, "invalid_token", "authentication required")
		return nil, false
	}
	for _, scope := range claims.Scopes {
		if scope == requiredScope {
			return claims, true
		}
	}
	w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+requiredScope+`"`)
	writeError(w, http.StatusForbidden, "insufficient_scope", "required OAuth scope is not granted")
	return nil, false
}

// tmfAccountAllowed enforces tenant/account binding. Cross-account access is
// deliberately reported as 404 so a scoped integration cannot use resource
// identifiers or account IDs as an existence oracle.
func tmfAccountAllowed(w http.ResponseWriter, claims *auth.Claims, accountID string) bool {
	accountID = strings.TrimSpace(accountID)
	if claims != nil && claims.GlobalAccess {
		return true
	}
	if accountID != "" && claims != nil {
		for _, allowed := range claims.AccountIDs {
			if allowed == accountID {
				return true
			}
		}
	}
	writeError(w, http.StatusNotFound, "ErrNotFound", "resource not found")
	return false
}

func (h *handler) authorizeTMF(w http.ResponseWriter, r *http.Request, requiredScope, accountID string) bool {
	claims, ok := h.tmfPrincipal(w, r, requiredScope)
	if !ok {
		return false
	}
	return tmfAccountAllowed(w, claims, accountID)
}
