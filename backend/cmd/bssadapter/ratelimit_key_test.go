package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/observability"
	"acs/internal/ratelimit"
)

func rateLimitedStack(t *testing.T, burst int) (http.Handler, []byte) {
	t.Helper()
	secret := []byte("test-oauth-signing-secret-0123456789abcdef")
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	// Refill is negligible within a test: only the burst is available.
	limiter := ratelimit.New(0.001, burst, time.Minute)
	notRevoked := func(context.Context, string) bool { return false }
	return withAuth("", secret, notRevoked, withRateLimit(limiter, observability.NewMetrics(t.Name()), ok)), secret
}

func tokenFor(t *testing.T, secret []byte, clientID string, n int) string {
	t.Helper()
	now := time.Now()
	// A different expiry per call stands in for "minted at another moment":
	// every token is a different string.
	tok, err := auth.SignJWT(secret, auth.Claims{
		Subject: "bss-client:" + clientID, Role: bssClientRole, IssuedAt: now, ExpiresAt: now.Add(time.Hour + time.Duration(n)*time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// A client that mints a new access token for every request is still one
// caller: all its tokens draw on one allowance.
func TestRateLimitIsPerClientNotPerToken(t *testing.T) {
	stack, secret := rateLimitedStack(t, 3)
	call := func(clientID string, n int) int {
		req := httptest.NewRequest(http.MethodGet, "/bss/v1/devices", nil)
		req.Header.Set("Authorization", "Bearer "+tokenFor(t, secret, clientID, n))
		rec := httptest.NewRecorder()
		stack.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 3; i++ {
		if code := call("client-a", i); code != http.StatusNoContent {
			t.Fatalf("request %d → %d, want 204", i, code)
		}
	}
	if code := call("client-a", 99); code != http.StatusTooManyRequests {
		t.Fatalf("a fresh token got a fresh allowance: %d, want 429", code)
	}
	// Another client is unaffected.
	if code := call("client-b", 0); code != http.StatusNoContent {
		t.Errorf("client-b → %d, want 204", code)
	}
}

// Guessing client secrets at the token endpoint is limited per caller
// address, however the guesses are presented.
func TestTokenEndpointGuessesShareOneAllowance(t *testing.T) {
	stack, _ := rateLimitedStack(t, 3)
	guess := func(n int, basic bool) int {
		req := httptest.NewRequest(http.MethodPost, "/bss/v1/oauth/token", nil)
		req.RemoteAddr = "203.0.113.7:4444"
		if basic {
			req.SetBasicAuth("bss-client-id", fmt.Sprintf("guess-%d", n)) // a different header every time
		}
		rec := httptest.NewRecorder()
		stack.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 3; i++ {
		if code := guess(i, true); code != http.StatusNoContent {
			t.Fatalf("guess %d → %d, want through to the handler", i, code)
		}
	}
	if code := guess(3, true); code != http.StatusTooManyRequests {
		t.Fatalf("a new Basic header got a new allowance: %d, want 429", code)
	}
	if code := guess(4, false); code != http.StatusTooManyRequests {
		t.Fatalf("dropping the header got a new allowance: %d, want 429", code)
	}
}

// With the shared limit on, a caller refused across the replicas is
// refused here too, whatever this process's own bucket says (finding
// O10). Off (nil), requests pass as before.
func TestSharedRateLimitAppliesBehindThePerProcessLimit(t *testing.T) {
	saved := sharedRateLimit
	t.Cleanup(func() { sharedRateLimit = saved })
	served := 0
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served++ })
	h := withRateLimit(ratelimit.New(1000, 1000, time.Minute), observability.NewMetrics(t.Name()), ok)
	get := func() int {
		req := httptest.NewRequest(http.MethodGet, "/bss/v1/devices", nil)
		req.RemoteAddr = "203.0.113.9:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("shared limit off = %d", code)
	}
	sharedRateLimit = ratelimit.NewSynced(nil, "bss", 1, 1, time.Hour)
	if code := get(); code != http.StatusOK {
		t.Fatalf("shared limit on, caller not over it = %d", code)
	}
	sharedRateLimit.BlockUntil("addr:203.0.113.9", time.Now().Add(time.Minute))
	if code := get(); code != http.StatusTooManyRequests {
		t.Fatalf("caller over the shared limit = %d, want 429", code)
	}
	if served != 2 {
		t.Fatalf("handler ran %d times, want 2", served)
	}
}
