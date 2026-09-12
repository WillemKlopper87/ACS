// Package mtp's allowlist.go: the network-level half of the USP agent
// allowlist (design docs/superpowers/specs/2026-09-12-usp-agent-allowlist-design.md
// S2.1). A remote IP outside the configured CIDRs never completes a
// protocol handshake on either transport.
package mtp

import (
	"log/slog"
	"net"
)

// ipAllowed reports whether ip is permitted by cidrs: true when cidrs is
// empty (permissive, matching internal/netguard's own
// default-permissive-until-configured convention for CIDR allowlists
// elsewhere in this codebase), or when ip falls inside at least one of
// them.
//
// Deliberately not internal/netguard.Policy.CheckIP: that method also
// always forbids loopback/link-local/multicast/unspecified addresses
// regardless of policy -- a rule that exists to stop operator-influenced
// OUTBOUND connections from reaching internal targets (SSRF). Loopback is
// a normal, expected source for an inbound USP agent connection in local
// development, CI, and a same-host deployment, so this check has no such
// always-forbidden classes.
func ipAllowed(cidrs []*net.IPNet, ip net.IP) bool {
	if len(cidrs) == 0 {
		return true
	}
	for _, n := range cidrs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// filteringListener wraps a net.Listener so Accept only ever returns a
// connection whose remote IP is allowed by cidrs. A disallowed connection
// is accepted (so it can be closed cleanly rather than left to the OS to
// eventually reset) and then immediately closed without being returned --
// the peer sees a TCP connection that opens and closes with no data
// exchanged, and Accept loops to the next pending connection rather than
// surfacing an error for what is, from the caller's perspective, routine
// traffic to expect and ignore.
type filteringListener struct {
	net.Listener
	cidrs []*net.IPNet
	log   *slog.Logger
}

func (fl *filteringListener) Accept() (net.Conn, error) {
	for {
		conn, err := fl.Listener.Accept()
		if err != nil {
			return nil, err
		}
		host, _, splitErr := net.SplitHostPort(conn.RemoteAddr().String())
		var ip net.IP
		if splitErr == nil {
			ip = net.ParseIP(host)
		}
		if ip == nil || !ipAllowed(fl.cidrs, ip) {
			fl.log.Warn("mtp: rejecting connection from a disallowed network", "remote_addr", conn.RemoteAddr().String())
			_ = conn.Close()
			continue
		}
		return conn, nil
	}
}

// wrapWithAllowlist returns ln unchanged when cidrs is empty (no
// wrapping overhead for the common permissive case), or a
// *filteringListener otherwise.
func wrapWithAllowlist(ln net.Listener, cidrs []*net.IPNet, log *slog.Logger) net.Listener {
	if len(cidrs) == 0 {
		return ln
	}
	return &filteringListener{Listener: ln, cidrs: cidrs, log: log}
}
