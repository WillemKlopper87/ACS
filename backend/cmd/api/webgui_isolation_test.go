package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A device reached through the web GUI proxy must not receive the
// operator's ticket or session: it can reach this API itself, and either
// would open other devices' consoles for as long as it lasts.
func TestWebGUIProxyDoesNotForwardOperatorCredentials(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://device.example/status.html?token=ticket-abc&page=2", nil)
	req.Header.Set("Authorization", "Bearer operator-session")
	req.Header.Set("Cookie", "device_session=1")
	stripOperatorCredentials(req)

	if got := req.URL.Query().Get("token"); got != "" {
		t.Errorf("ticket forwarded to the device: %q", got)
	}
	if got := req.URL.Query().Get("page"); got != "2" {
		t.Errorf("the device's own query parameter was lost: %q", req.URL.RawQuery)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization forwarded to the device: %q", got)
	}
	if got := req.Header.Get("Cookie"); got == "" {
		t.Error("the device's own cookie was dropped")
	}
}

// Device pages are served from this API's origin, so they are sandboxed
// into an origin of their own, whatever headers the device sent.
func TestWebGUIProxySandboxesDeviceContent(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Security-Policy", "default-src *") // a device trying to set its own policy
	h.Add("Set-Cookie", "evil=1; Path=/")
	isolateDeviceContent(h, false)

	if got := h.Values("Content-Security-Policy"); len(got) != 1 || got[0] != webGUISandbox {
		t.Errorf("Content-Security-Policy = %q, want only the sandbox", got)
	}
	for _, token := range []string{"allow-same-origin", "allow-top-navigation"} {
		if strings.Contains(webGUISandbox, token) {
			t.Errorf("the sandbox grants %s", token)
		}
	}
	if h.Get("Set-Cookie") != "" {
		t.Error("a device could set a cookie on the API origin")
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}

	// The lab opt-out drops the sandbox but keeps the other headers.
	lab := http.Header{}
	lab.Add("Set-Cookie", "session=1")
	isolateDeviceContent(lab, true)
	if lab.Get("Content-Security-Policy") != "" || lab.Get("Set-Cookie") == "" || lab.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("same-origin opt-out headers: %v", lab)
	}
}
