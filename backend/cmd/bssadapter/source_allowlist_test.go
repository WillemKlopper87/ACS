package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"acs/internal/clientip"
	"acs/internal/netguard"
)

func allowlistStack(t *testing.T, cidrs string) http.Handler {
	t.Helper()
	nets, err := netguard.ParseCIDRList(cidrs)
	if err != nil {
		t.Fatal(err)
	}
	reached := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return withSourceAllowlist(nets, slog.New(slog.NewTextHandler(io.Discard, nil)), reached)
}

func from(stack http.Handler, remote, path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)
	return rec
}

// The integration surface answers only the platform's networks, and it
// does so before authentication is looked at.
func TestSourceAllowlist(t *testing.T) {
	stack := allowlistStack(t, "198.51.100.0/24,2001:db8:42::/64")

	for _, c := range []struct {
		remote, path string
		want         int
	}{
		{"198.51.100.7:5000", "/bss/v1/devices", http.StatusNoContent},
		{"[2001:db8:42::9]:5000", "/tmf-api/serviceOrdering/v4/serviceOrder", http.StatusNoContent},
		{"203.0.113.9:5000", "/bss/v1/devices", http.StatusForbidden},
		{"203.0.113.9:5000", "/bss/v1/oauth/token", http.StatusForbidden},
		{"203.0.113.9:5000", "/tmf-api/serviceOrdering/v4/serviceOrder", http.StatusForbidden},
		// Not the integration surface: left to the listener's binding.
		{"203.0.113.9:5000", "/healthz", http.StatusNoContent},
		{"203.0.113.9:5000", "/readyz", http.StatusNoContent},
	} {
		if got := from(stack, c.remote, c.path).Code; got != c.want {
			t.Errorf("%s from %s → %d, want %d", c.path, c.remote, got, c.want)
		}
	}

	refused := from(stack, "203.0.113.9:5000", "/bss/v1/devices")
	if !strings.Contains(refused.Body.String(), "ErrSourceNotAllowed") {
		t.Errorf("refusal body: %s", refused.Body.String())
	}
}

// A caller cannot talk its way in with a forwarding header: it counts only
// when it comes from a proxy the deployment trusts.
func TestSourceAllowlistIgnoresUntrustedForwardingHeader(t *testing.T) {
	stack := allowlistStack(t, "198.51.100.0/24")
	if got := from(stack, "203.0.113.9:5000", "/bss/v1/devices", "X-Forwarded-For", "198.51.100.7").Code; got != http.StatusForbidden {
		t.Fatalf("a spoofed X-Forwarded-For was believed: %d", got)
	}

	// Behind the trusted proxy, the address it reports is the one checked.
	if err := clientip.Configure("127.0.0.1/32"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientip.Configure("") })
	if got := from(stack, "127.0.0.1:4000", "/bss/v1/devices", "X-Forwarded-For", "198.51.100.7").Code; got != http.StatusNoContent {
		t.Errorf("allowed caller behind the trusted proxy → %d, want through", got)
	}
	if got := from(stack, "127.0.0.1:4000", "/bss/v1/devices", "X-Forwarded-For", "203.0.113.9").Code; got != http.StatusForbidden {
		t.Errorf("outside caller behind the trusted proxy → %d, want 403", got)
	}
}

// With no list configured (lab), nothing is refused.
func TestSourceAllowlistUnsetAllowsAll(t *testing.T) {
	if got := from(allowlistStack(t, ""), "203.0.113.9:5000", "/bss/v1/devices").Code; got != http.StatusNoContent {
		t.Errorf("unset allowlist refused a caller: %d", got)
	}
}
