package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"acs/internal/connreq"
	"acs/internal/netguard"
)

// TestConnectionRequestWorker_NetworkPolicyRejectsLoopback is the P1.2/H-2
// acceptance gate: ConnectionRequestURL is CPE-controlled, so a device
// pointing it at a host outside the configured device-network policy
// must be refused before the ACS ever GETs it — not merely reported as a
// normal network failure. Loopback (what httptest.NewServer binds to) is
// refused unconditionally by netguard regardless of any allowlist.
func TestConnectionRequestWorker_NetworkPolicyRejectsLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("device network policy should have refused this target before the ACS ever dialed it")
	}))
	defer server.Close()

	w := &connectionRequestWorker{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		netPolicy: netguard.Policy{},
	}

	got := w.attemptGET(t.Context(), server.URL+"/cwmp", "user", "pass")
	if got != connreq.OutcomeBlockedByPolicy {
		t.Errorf("attemptGET() = %q, want %q", got, connreq.OutcomeBlockedByPolicy)
	}
}

// TestConnectionRequestWorker_NetworkPolicyRejectsHostOutsideAllowlist
// proves the same gate for a non-loopback hostname when an explicit
// allowlist is configured and the target falls outside it.
func TestConnectionRequestWorker_NetworkPolicyRejectsHostOutsideAllowlist(t *testing.T) {
	cidrs, err := netguard.ParseCIDRList("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	w := &connectionRequestWorker{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		netPolicy: netguard.Policy{AllowedCIDRs: cidrs},
	}

	// A literal public IP outside 10.0.0.0/8 — a literal address so this
	// test doesn't depend on DNS resolution succeeding in CI, while still
	// exercising CheckHost's CIDR-allowlist path rather than the
	// always-forbidden loopback case the other test covers.
	got := w.attemptGET(t.Context(), "http://8.8.8.8/cwmp", "", "")
	if got != connreq.OutcomeBlockedByPolicy {
		t.Errorf("attemptGET() = %q, want %q", got, connreq.OutcomeBlockedByPolicy)
	}
}
