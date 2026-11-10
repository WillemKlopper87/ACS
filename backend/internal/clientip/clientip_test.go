package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func req(peer string, headers map[string]string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = peer
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestDefaultIgnoresForwardingHeaders(t *testing.T) {
	// Nobody trusted: a client choosing its own address must not work.
	r, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	got := r.IP(req("203.0.113.9:5555", map[string]string{"X-Forwarded-For": "198.51.100.2"}))
	if got != "203.0.113.9" {
		t.Fatalf("untrusted peer's headers were believed: %s", got)
	}
	var zero *Resolver
	if got := zero.IP(req("203.0.113.9:1", map[string]string{"X-Forwarded-For": "198.51.100.1"})); got != "203.0.113.9" {
		t.Fatalf("nil resolver = %s", got)
	}
}

func TestTrustedProxy(t *testing.T) {
	r, err := Parse("127.0.0.1, ::1, 10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		peer    string
		headers map[string]string
		want    string
	}{
		{"one forwarded client", "127.0.0.1:41000", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "198.51.100.7"},
		{"ipv6 client", "127.0.0.1:41000", map[string]string{"X-Forwarded-For": "2001:db8::5"}, "2001:db8::5"},
		{"a pass-through CF header is not believed", "127.0.0.1:1", map[string]string{"CF-Connecting-IP": "6.6.6.6"}, "127.0.0.1"},
		{"forged left entries are ignored", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "6.6.6.6, 7.7.7.7, 198.51.100.9"}, "198.51.100.9"},
		{"xff right-most untrusted", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "6.6.6.6, 198.51.100.8, 10.1.2.3"}, "198.51.100.8"},
		{"xff all trusted falls back to the peer", "10.0.0.5:1", map[string]string{"X-Forwarded-For": "10.1.1.1, 10.2.2.2"}, "10.0.0.5"},
		{"bad header falls back to the peer", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "not-an-ip"}, "127.0.0.1"},
		{"no header", "127.0.0.1:1", nil, "127.0.0.1"},
		{"peer outside the trusted ranges", "192.0.2.50:1", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "192.0.2.50"},
		{"v4-mapped peer", "[::ffff:127.0.0.1]:1", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "198.51.100.7"},
	}
	for _, c := range cases {
		if got := r.IP(req(c.peer, c.headers)); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse("127.0.0.1, banana"); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestConfigureAndOf(t *testing.T) {
	if err := Configure("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Configure("") })
	if got := Of(req("127.0.0.1:9", map[string]string{"X-Forwarded-For": "198.51.100.7"})); got != "198.51.100.7" {
		t.Fatalf("Of = %s", got)
	}
	if err := Configure("nope"); err == nil {
		t.Fatal("bad config accepted")
	}
}
