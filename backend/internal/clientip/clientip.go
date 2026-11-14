// Package clientip decides which network address a request really came from.
//
// By default that is the TCP peer, and forwarding headers are ignored: any
// client can send them, so trusting them would let it pick its own rate-limit
// key. Behind a reverse proxy or tunnel the peer is always the proxy, which
// makes every user look like one client (a shared login throttle, a shared
// rate-limit bucket). ACS_TRUSTED_PROXY_CIDRS names the proxies whose
// forwarding headers may be believed; only a request whose peer is inside
// one of those ranges is looked at further.
package clientip

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
)

// Resolver holds the trusted proxy ranges. The zero value trusts nobody.
type Resolver struct {
	trusted []netip.Prefix
}

// Parse reads a comma-separated list of CIDR ranges or single addresses
// (127.0.0.1, 10.0.0.0/8, ::1). An empty list trusts nobody.
func Parse(csv string) (*Resolver, error) {
	r := &Resolver{}
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			r.trusted = append(r.trusted, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q is not an address or CIDR range", part)
		}
		a = a.Unmap()
		r.trusted = append(r.trusted, netip.PrefixFrom(a, a.BitLen()))
	}
	return r, nil
}

func (r *Resolver) isTrusted(a netip.Addr) bool {
	if r == nil {
		return false
	}
	a = a.Unmap()
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// IP is the client address of req, without a port. Behind a trusted proxy it
// is the right-most X-Forwarded-For entry that is not itself a trusted proxy
// (the standard safe reading: entries to the left of the first address a
// trusted proxy appended can be forged by the client); in every other case it
// is the TCP peer. Only X-Forwarded-For is read: a special header such as
// CF-Connecting-IP would be believed from a proxy that passes it through
// untouched.
func (r *Resolver) IP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !r.isTrusted(peer) {
		return host
	}
	parts := strings.Split(strings.Join(req.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, ok := parseEntry(parts[i])
		if !ok {
			// A trusted proxy wrote this entry in a form we cannot read, so
			// everything to its left is only the client's word. Stop and use
			// the peer rather than skip it and believe a forged address.
			return host
		}
		if r.isTrusted(a) {
			continue
		}
		return a.Unmap().String()
	}
	return host
}

// parseEntry reads one X-Forwarded-For entry: an address, or an address with
// a port ("192.0.2.1:443", "[2001:db8::1]:443") as some load balancers add.
func parseEntry(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return a, true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr(), true
	}
	return netip.Addr{}, false
}

var current atomic.Pointer[Resolver]

// Configure sets the process-wide resolver from ACS_TRUSTED_PROXY_CIDRS.
func Configure(csv string) error {
	r, err := Parse(csv)
	if err != nil {
		return err
	}
	current.Store(r)
	return nil
}

// Of is the client address of req under the process-wide configuration.
func Of(req *http.Request) string { return current.Load().IP(req) }
