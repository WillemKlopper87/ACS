package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProtectMetrics(t *testing.T) {
	served := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("metrics")) })
	get := func(h http.Handler, authorization string) int {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	// No token configured: open, as before.
	if code := get(ProtectMetrics("", served), ""); code != 200 {
		t.Fatalf("no token configured = %d", code)
	}
	h := ProtectMetrics("scrape-token-0123456789", served)
	for name, c := range map[string]struct {
		authorization string
		want          int
	}{
		"no credential":   {"", 401},
		"wrong token":     {"Bearer something-else", 401},
		"token as basic":  {"Basic scrape-token-0123456789", 401},
		"prefix of token": {"Bearer scrape-token", 401},
		"the token":       {"Bearer scrape-token-0123456789", 200},
	} {
		if code := get(h, c.authorization); code != c.want {
			t.Errorf("%s = %d, want %d", name, code, c.want)
		}
	}
}
