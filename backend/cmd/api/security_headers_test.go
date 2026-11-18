package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersOnEveryAnswerButTheDeviceProxy(t *testing.T) {
	ownCache := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("download") != "" {
			w.Header().Set("Cache-Control", "private, max-age=60")
		}
	})
	get := func(target string) http.Header {
		rec := httptest.NewRecorder()
		withSecurityHeaders(ownCache).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec.Header()
	}
	h := get("/api/v1/devices")
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("an API answer: %v", h)
	}
	if h := get("/api/v1/firmware/images/x?download=1"); h.Get("Cache-Control") != "private, max-age=60" {
		t.Errorf("a handler's own Cache-Control was overridden: %v", h)
	}
	if h := get("/healthz"); h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "" {
		t.Errorf("outside /api/: %v", h)
	}
	// A device's own pages are relayed as the device sent them.
	if h := get("/api/v1/devices/d1/webgui/proxy/index.html"); len(h) != 0 {
		t.Errorf("the device proxy got headers: %v", h)
	}
}
