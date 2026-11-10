package main

import (
	"net/http/httptest"
	"testing"

	"acs/internal/clientip"
)

// Behind a trusted proxy the login throttle and rate limit must key on the
// real client, not on the proxy; without trust a client-chosen header must
// change nothing.
func TestClientIPBehindTrustedProxy(t *testing.T) {
	t.Cleanup(func() { _ = clientip.Configure("") })
	r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")

	if got := clientIP(r); got != "127.0.0.1" {
		t.Fatalf("no trusted proxy configured: clientIP = %s, want the peer", got)
	}
	if err := clientip.Configure("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if got := clientIP(r); got != "198.51.100.7" {
		t.Fatalf("trusted proxy: clientIP = %s, want the forwarded client", got)
	}
	r.RemoteAddr = "203.0.113.5:50000"
	if got := clientIP(r); got != "203.0.113.5" {
		t.Fatalf("a peer that is not a trusted proxy chose its own address: %s", got)
	}
}
