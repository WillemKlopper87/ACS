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

// A target an outside party chooses (a webhook receiver) may not be on a
// private network unless the operator listed it.
func TestForbidPrivateRefusesPrivateTargetsUnlessListed(t *testing.T) {
	strict := Policy{ForbidPrivate: true}
	for _, addr := range []string{"10.0.0.5", "172.17.0.1", "192.168.1.10", "100.64.3.2", "fd12:3456::1"} {
		if err := strict.CheckIP(net.ParseIP(addr)); err == nil {
			t.Errorf("%s was allowed", addr)
		}
	}
	for _, addr := range []string{"203.0.113.9", "8.8.8.8", "2001:db8::1"} {
		if err := strict.CheckIP(net.ParseIP(addr)); err != nil {
			t.Errorf("%s was refused: %v", addr, err)
		}
	}
	// Without the flag nothing changes for the other users of a policy.
	if err := (Policy{}).CheckIP(net.ParseIP("10.0.0.5")); err != nil {
		t.Errorf("a plain policy refused a private address: %v", err)
	}
	// An allowlist is the operator's stated intent and takes precedence.
	_, n, _ := net.ParseCIDR("10.20.0.0/16")
	listed := Policy{ForbidPrivate: true, AllowedCIDRs: []*net.IPNet{n}}
	if err := listed.CheckIP(net.ParseIP("10.20.1.1")); err != nil {
		t.Errorf("a listed private address was refused: %v", err)
	}
	if err := listed.CheckIP(net.ParseIP("10.30.1.1")); err == nil {
		t.Error("an unlisted address passed an allowlist")
	}
	if err := listed.CheckIP(net.ParseIP("127.0.0.1")); err == nil {
		t.Error("loopback passed")
	}
}
