package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/bss"
)

func tmfTestToken(t *testing.T, secret []byte, scopes, accounts []string, global bool) string {
	t.Helper()
	now := time.Now().UTC()
	token, err := auth.SignJWT(secret, auth.Claims{
		Subject: "bss-client:test-client", Role: bssClientRole,
		IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		Scopes: scopes, AccountIDs: accounts, GlobalAccess: global,
	})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	return token
}

func TestTMFPrincipalRequiresGrantedScope(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	h := &handler{oauthSigningSecret: secret}
	r := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceInventoryManagement/v4/service?accountId=acct-a", nil)
	r.Header.Set("Authorization", "Bearer "+tmfTestToken(t, secret, []string{bss.ScopeTMFRead}, []string{"acct-a"}, false))

	rr := httptest.NewRecorder()
	if _, ok := h.tmfPrincipal(rr, r, bss.ScopeTMFWrite); ok {
		t.Fatal("tmfPrincipal accepted a token without required write scope")
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("missing WWW-Authenticate insufficient_scope challenge")
	}
}

func TestTMFAccountAllowedDoesNotLeakCrossTenant(t *testing.T) {
	claims := &auth.Claims{Scopes: []string{bss.ScopeTMFRead}, AccountIDs: []string{"acct-a"}}

	rr := httptest.NewRecorder()
	if !tmfAccountAllowed(rr, claims, "acct-a") {
		t.Fatal("authorized account was rejected")
	}

	rr = httptest.NewRecorder()
	if tmfAccountAllowed(rr, claims, "acct-b") {
		t.Fatal("cross-tenant account was accepted")
	}
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant status = %d, want non-leaking 404", rr.Code)
	}
}

func TestTMFGlobalAndDevelopmentPrincipals(t *testing.T) {
	rr := httptest.NewRecorder()
	if !tmfAccountAllowed(rr, &auth.Claims{GlobalAccess: true}, "any-account") {
		t.Fatal("global principal rejected")
	}

	h := &handler{}
	r := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceInventoryManagement/v4/service", nil)
	rr = httptest.NewRecorder()
	claims, ok := h.tmfPrincipal(rr, r, bss.ScopeTMFExecute)
	if !ok || !claims.GlobalAccess {
		t.Fatalf("no-auth development mode principal = %+v, ok=%v", claims, ok)
	}
}

func TestTMFLegacyTokenRetainsExplicitCompatibilityAccess(t *testing.T) {
	h := &handler{token: "legacy-shared-token"}
	r := httptest.NewRequest(http.MethodGet, "/tmf-api/serviceInventoryManagement/v4/service", nil)
	r.Header.Set("Authorization", "Bearer legacy-shared-token")
	rr := httptest.NewRecorder()
	claims, ok := h.tmfPrincipal(rr, r, bss.ScopeTMFAcknowledge)
	if !ok || !claims.GlobalAccess {
		t.Fatalf("legacy compatibility principal = %+v, ok=%v", claims, ok)
	}
}
