package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Standalone: the process looks healthy to whatever started it, and every
// integration route says, in so many words, that the integration is off.
func TestStandaloneAdapterRefusesIntegrationRoutes(t *testing.T) {
	h := standaloneHandler()
	get := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer anything")
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		if rec := get(http.MethodGet, path); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "standalone") {
			t.Errorf("%s = %d %s", path, rec.Code, rec.Body)
		}
	}
	for _, c := range [][2]string{
		{http.MethodPost, "/bss/v1/oauth/token"},
		{http.MethodPost, "/bss/v1/orders"},
		{http.MethodGet, "/bss/v1/devices"},
		{http.MethodPost, "/tmf-api/serviceOrdering/v4/serviceOrder"},
		{http.MethodGet, "/metrics"},
	} {
		rec := get(c[0], c[1])
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ErrIntegrationDisabled") {
			t.Errorf("%s %s = %d %s, want 503 ErrIntegrationDisabled", c[0], c[1], rec.Code, rec.Body)
		}
	}
}
