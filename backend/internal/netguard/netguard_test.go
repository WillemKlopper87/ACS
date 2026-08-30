package netguard

import (
	"net"
	"testing"
)

func mustCIDRs(t *testing.T, s string) []*net.IPNet {
	t.Helper()
	out, err := ParseCIDRList(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAlwaysForbiddenClasses(t *testing.T) {
	p := Policy{} // no allowlist — only the hard classes apply
	for _, addr := range []string{"127.0.0.1", "::1", "169.254.169.254", "169.254.1.1", "224.0.0.1", "0.0.0.0", "fe80::1", "ff02::1", "::"} {
		if err := p.CheckIP(net.ParseIP(addr)); err == nil {
			t.Errorf("CheckIP(%s) = nil, want error", addr)
		}
	}
}

func TestNoAllowlistPermitsOrdinaryAddresses(t *testing.T) {
	p := Policy{}
	for _, addr := range []string{"10.99.0.5", "192.168.1.1", "203.0.113.9", "2001:db8::1"} {
		if err := p.CheckIP(net.ParseIP(addr)); err != nil {
			t.Errorf("CheckIP(%s) = %v, want nil", addr, err)
		}
	}
}

func TestAllowlistRestricts(t *testing.T) {
	p := Policy{AllowedCIDRs: mustCIDRs(t, "10.99.0.0/16,192.168.0.0/24")}
	if err := p.CheckIP(net.ParseIP("10.99.3.7")); err != nil {
		t.Errorf("in-allowlist address rejected: %v", err)
	}
	if err := p.CheckIP(net.ParseIP("192.168.0.44")); err != nil {
		t.Errorf("in-allowlist address rejected: %v", err)
	}
	for _, addr := range []string{"10.0.0.1", "192.168.1.44", "8.8.8.8"} {
		if err := p.CheckIP(net.ParseIP(addr)); err == nil {
			t.Errorf("CheckIP(%s) = nil, want error (outside allowlist)", addr)
		}
	}
	// Hard classes stay forbidden even when an allowlist would contain them.
	p2 := Policy{AllowedCIDRs: mustCIDRs(t, "127.0.0.0/8,169.254.0.0/16")}
	for _, addr := range []string{"127.0.0.1", "169.254.169.254"} {
		if err := p2.CheckIP(net.ParseIP(addr)); err == nil {
			t.Errorf("CheckIP(%s) = nil, want error (hard class overrides allowlist)", addr)
		}
	}
}

func TestParseCIDRList(t *testing.T) {
	if _, err := ParseCIDRList("10.0.0.0/8,not-a-cidr"); err == nil {
		t.Error("ParseCIDRList with junk = nil, want error")
	}
	out, err := ParseCIDRList("")
	if err != nil || out != nil {
		t.Errorf("ParseCIDRList(\"\") = %v, %v; want nil, nil", out, err)
	}
}

func TestDialControl(t *testing.T) {
	p := Policy{AllowedCIDRs: mustCIDRs(t, "10.99.0.0/16")}
	if err := p.DialControl("tcp4", "10.99.0.2:80", nil); err != nil {
		t.Errorf("DialControl in-scope = %v, want nil", err)
	}
	if err := p.DialControl("tcp4", "169.254.169.254:80", nil); err == nil {
		t.Error("DialControl to metadata address = nil, want error")
	}
	if err := p.DialControl("tcp4", "8.8.8.8:443", nil); err == nil {
		t.Error("DialControl outside allowlist = nil, want error")
	}
}
