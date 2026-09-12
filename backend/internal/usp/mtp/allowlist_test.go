package mtp

import (
	"log/slog"
	"net"
	"testing"
)

func mustParseCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("parse CIDR %q: %v", s, err)
	}
	return n
}

func TestIPAllowedEmptyIsPermissive(t *testing.T) {
	if !ipAllowed(nil, net.ParseIP("203.0.113.5")) {
		t.Error("ipAllowed with no CIDRs = false, want true (permissive default)")
	}
}

func TestIPAllowedContainment(t *testing.T) {
	cidrs := []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")}
	if !ipAllowed(cidrs, net.ParseIP("10.1.2.3")) {
		t.Error("ipAllowed(10.1.2.3) inside 10.0.0.0/8 = false, want true")
	}
	if ipAllowed(cidrs, net.ParseIP("203.0.113.5")) {
		t.Error("ipAllowed(203.0.113.5) outside 10.0.0.0/8 = true, want false")
	}
}

func TestIPAllowedLoopbackNotSpeciallyForbidden(t *testing.T) {
	// Unlike netguard.Policy.CheckIP, loopback is ordinary here: allowed
	// when it falls in an explicit CIDR, not unconditionally forbidden.
	cidrs := []*net.IPNet{mustParseCIDR(t, "127.0.0.0/8")}
	if !ipAllowed(cidrs, net.ParseIP("127.0.0.1")) {
		t.Error("ipAllowed(127.0.0.1) inside an explicit 127.0.0.0/8 allowlist = false, want true")
	}
}

// TestFilteringListenerRejectsDisallowedRemote proves the WebSocket
// listener wrapper actually rejects at the TCP-accept level: a CIDR that
// deliberately excludes loopback closes a real local dial before any WS
// upgrade response.
func TestFilteringListenerRejectsDisallowedRemote(t *testing.T) {
	ws, err := NewWebSocket(WebSocketConfig{
		Addr:           "127.0.0.1:0",
		AllowPlaintext: true,
		AllowedCIDRs:   []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")}, // excludes 127.0.0.1
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	_, url := startedWS(t, ws)
	if _, _, err := dialRaw(url); err == nil {
		t.Error("dial from disallowed 127.0.0.1 succeeded, want a connection error")
	}
}

// TestFilteringListenerAllowsPermittedRemote proves the positive case:
// a CIDR that includes loopback lets a real local dial complete its WS
// upgrade normally.
func TestFilteringListenerAllowsPermittedRemote(t *testing.T) {
	ws, err := NewWebSocket(WebSocketConfig{
		Addr:           "127.0.0.1:0",
		AllowPlaintext: true,
		AllowedCIDRs:   []*net.IPNet{mustParseCIDR(t, "127.0.0.0/8")},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	h := newRecordingHandler()
	_, url := startedWSWithHandler(t, ws, h)
	// dial needs a valid eid query parameter (R-WS.10b/10c) or the
	// handler 400s before ever reaching the allowlist-gated Accept this
	// test means to exercise; url alone (no query) 400s regardless of
	// AllowedCIDRs, same as TestWebSocketMissingEIDRejected.
	c := dial(t, url+"?eid=os%3A%3A012345-AAAA", "v1.usp")
	defer c.CloseNow()
	waitFor(t, h.connected, "OnConnect after an allowed dial")
}
