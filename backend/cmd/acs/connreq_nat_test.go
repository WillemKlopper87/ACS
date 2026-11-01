package main

import "testing"

func TestConnectionRequestBehindNAT(t *testing.T) {
	for _, tc := range []struct {
		name, url, source string
		behind, known     bool
	}{
		// Nokia FastMile on a carrier network (field test 2026-10-30).
		{"carrier CGNAT", "http://10.10.178.14:7547/", "66.18.65.65", true, true},
		{"mesh satellite on gateway LAN", "http://192.168.1.48:7547", "66.18.65.65", true, true},
		// On-premises ACS on a private management network: directly reachable.
		{"private but direct", "http://10.20.0.15:7547/", "10.20.0.15", false, true},
		{"public direct", "http://203.0.113.7:7547/", "203.0.113.7", false, true},
		{"ipv6 direct", "http://[2001:db8::1]:7547/", "2001:db8::1", false, true},
		{"hostname cannot be compared", "http://cpe.example.net:7547/", "203.0.113.7", false, false},
		{"garbage", "::not a url::", "203.0.113.7", false, false},
	} {
		behind, known := connectionRequestBehindNAT(tc.url, tc.source)
		if behind != tc.behind || known != tc.known {
			t.Errorf("%s: connectionRequestBehindNAT(%q, %q) = (%v, %v), want (%v, %v)", tc.name, tc.url, tc.source, behind, known, tc.behind, tc.known)
		}
	}
}
