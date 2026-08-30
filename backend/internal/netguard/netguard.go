// Package netguard restricts where operator-influenced outbound
// connections may go (audit P0.4). The device web-GUI proxy and the
// SSH/Telnet console bridge both dial addresses that ultimately come
// from operator input; without a policy they are server-side request
// forgery primitives able to reach anything the ACS host can — cloud
// metadata, the database, internal admin planes. The policy is checked
// both up front (resolving the hostname, for fast feedback) and at
// dial time on the literal connection address (so a DNS answer that
// changes between check and connect — rebinding — can't slip through).
package netguard

import (
	"context"
	"fmt"
	"net"
	"syscall"
)

// Policy says which target IPs are acceptable.
type Policy struct {
	// AllowedCIDRs, when non-empty, is an allowlist: a target must fall
	// inside one of these networks. When empty, any address passes
	// except the always-forbidden classes below.
	AllowedCIDRs []*net.IPNet
}

// ParseCIDRList parses a comma-separated CIDR list (e.g. the
// ACS_WEBGUI_ALLOWED_CIDRS environment variable).
func ParseCIDRList(s string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := s[start:i]
			start = i + 1
			if part == "" {
				continue
			}
			_, n, err := net.ParseCIDR(part)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q: %w", part, err)
			}
			out = append(out, n)
		}
	}
	return out, nil
}

// CheckIP validates one literal IP against the policy.
func (p Policy) CheckIP(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("target address is not an IP")
	}
	// Always-forbidden classes, allowlist or not: these are never a CPE.
	switch {
	case ip.IsLoopback():
		return fmt.Errorf("target %s is a loopback address", ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		// Covers 169.254.0.0/16 — including 169.254.169.254, the cloud
		// metadata service.
		return fmt.Errorf("target %s is a link-local address", ip)
	case ip.IsMulticast():
		return fmt.Errorf("target %s is a multicast address", ip)
	case ip.IsUnspecified():
		return fmt.Errorf("target %s is the unspecified address", ip)
	}
	if len(p.AllowedCIDRs) == 0 {
		return nil
	}
	for _, n := range p.AllowedCIDRs {
		if n.Contains(ip) {
			return nil
		}
	}
	return fmt.Errorf("target %s is outside the allowed device networks", ip)
}

// CheckHost resolves host (a hostname or literal IP, no port) and
// validates every address it resolves to — a hostname is only as
// trustworthy as its worst A/AAAA record.
func (p Policy) CheckHost(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		return p.CheckIP(ip)
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%q resolved to no addresses", host)
	}
	for _, a := range addrs {
		if err := p.CheckIP(a.IP); err != nil {
			return err
		}
	}
	return nil
}

// DialControl is a net.Dialer.Control function enforcing the policy on
// the literal address actually being connected to — the rebinding-proof
// backstop behind CheckHost.
func (p Policy) DialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("split dial address %q: %w", address, err)
	}
	return p.CheckIP(net.ParseIP(host))
}
