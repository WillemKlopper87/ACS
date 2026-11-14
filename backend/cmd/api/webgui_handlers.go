// Device web-GUI embed (admin-platform backlog, scaffolded "build now,
// functional later" — same CGNAT reachability constraint as SSH/Telnet).
// The proxy exists because the browser can only ever reach the ACS
// itself — the ACS has to be the one dialing the device's own admin UI,
// same reachability model as the SSH/Telnet bridge, and the same shape a
// future VPN overlay IP would need (a browser can't route to a private
// overlay address directly; the ACS, sitting on that overlay, can). This
// is a best-effort passthrough, not a general web proxy: it forwards
// requests and injects Basic Auth if configured, but does not rewrite
// absolute URLs a device's own JS might reference — fine for the simple
// embedded-device admin UIs this targets, not guaranteed for a complex SPA.
package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"acs/internal/cliaccess"
)

type setWebGUIConfigRequest struct {
	BaseURL  string `json:"base_url"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type webGUIConfigResponse struct {
	DeviceID  string `json:"device_id"`
	BaseURL   string `json:"base_url"`
	Username  string `json:"username,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

func toWebGUIConfigResponse(c cliaccess.WebGUIConfig) webGUIConfigResponse {
	return webGUIConfigResponse{
		DeviceID: c.DeviceID, BaseURL: c.BaseURL, Username: c.Username,
		UpdatedAt: c.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (h *handler) setWebGUIConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}

	var req setWebGUIConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	target, err := url.ParseRequestURI(req.BaseURL)
	if err != nil {
		http.Error(w, "base_url must be a valid absolute URL", http.StatusBadRequest)
		return
	}
	// SSRF policy (audit P0.4): only http/https, and only hosts the
	// device-network policy allows — checked again at dial time, this is
	// the fast-feedback copy.
	if target.Scheme != "http" && target.Scheme != "https" {
		http.Error(w, "base_url scheme must be http or https", http.StatusBadRequest)
		return
	}
	if err := h.netPolicy.CheckHost(r.Context(), target.Hostname()); err != nil {
		http.Error(w, "base_url host is not allowed: "+err.Error(), http.StatusBadRequest)
		return
	}

	cfg, err := h.cli.SetWebGUIConfig(r.Context(), id, req.BaseURL, req.Username, req.Password)
	if err != nil {
		h.logger.Error("failed to save webgui config", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), id, "WebGUIConfigured", map[string]any{"base_url": req.BaseURL}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusOK, toWebGUIConfigResponse(*cfg))
}

func (h *handler) getWebGUIConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	cfg, err := h.cli.GetWebGUIConfig(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to get webgui config", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if cfg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	writeJSON(w, http.StatusOK, toWebGUIConfigResponse(*cfg))
}

func (h *handler) deleteWebGUIConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	if err := h.cli.DeleteWebGUIConfig(r.Context(), id); err != nil {
		h.logger.Error("failed to delete webgui config", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// proxyWebGUI forwards everything under
// /api/v1/devices/{id}/webgui/proxy/{path...} to the device's configured
// base_url + {path...}, injecting Basic Auth if a username was configured.
// Registered outside route()/metrics.InstrumentHTTP for the same reason as
// the CLI WebSocket endpoint — an embedded device's asset-heavy admin UI
// generates many requests per page load, and per-request metrics labels
// here would be noise, not signal.
func (h *handler) proxyWebGUI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	cfg, err := h.cli.GetWebGUIConfig(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to get webgui config", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if cfg == nil {
		http.Error(w, "no web GUI configured for this device", http.StatusNotFound)
		return
	}
	target, err := url.Parse(cfg.BaseURL)
	if err != nil {
		http.Error(w, "device's configured base_url is not a valid URL", http.StatusInternalServerError)
		return
	}
	// SSRF policy (audit P0.4): scheme allowlist plus the device-network
	// policy — enforced here on stored configs too (a config saved
	// before the policy tightened doesn't get grandfathered in), and
	// again at dial time on the literal IP via DialControl below, which
	// is what defeats DNS rebinding between check and connect.
	if target.Scheme != "http" && target.Scheme != "https" {
		http.Error(w, "device web GUI scheme must be http or https", http.StatusForbidden)
		return
	}
	if err := h.netPolicy.CheckHost(r.Context(), target.Hostname()); err != nil {
		h.logger.Warn("webgui proxy target rejected by network policy", "err", err, "device_id", id, "base_url", cfg.BaseURL)
		http.Error(w, "device web GUI host is not allowed: "+err.Error(), http.StatusForbidden)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: h.netPolicy.DialControl,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	}
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		req.URL.Path = "/" + r.PathValue("path")
		originalDirector(req)
		// The operator's credential stops here. The browser ticket rides in
		// the query string and a console session in Authorization; passing
		// either on would hand a device (which can reach this API) a
		// credential that opens other devices' consoles for its lifetime.
		stripOperatorCredentials(req)
		if cfg.Username != "" {
			req.SetBasicAuth(cfg.Username, cfg.Password)
		}
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		isolateDeviceContent(resp.Header, h.webGUISameOrigin)
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.logger.Warn("webgui proxy request failed — device likely unreachable", "err", err, "device_id", id, "base_url", cfg.BaseURL)
		http.Error(w, "device web GUI unreachable: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// webGUISandbox is the Content-Security-Policy put on everything the web
// GUI proxy returns. A device's pages are served from this API's own
// origin, so without it a script on a compromised or malicious CPE runs
// with that origin's privileges: where the console shares the origin (the
// production layout) it can read the operator's session out of browser
// storage. "sandbox" without allow-same-origin gives the page an origin of
// its own: scripts and forms still work, but it cannot read this origin's
// storage or cookies, or reach the console that frames it.
const webGUISandbox = "sandbox allow-forms allow-scripts allow-popups"

// isolateDeviceContent sets the headers that keep proxied device content
// apart from the console. sameOrigin (ACS_WEBGUI_ALLOW_SAME_ORIGIN=true)
// drops the sandbox for a lab where a device GUI needs its own cookies or
// storage to work and the API is on a different origin from the console.
func isolateDeviceContent(h http.Header, sameOrigin bool) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if sameOrigin {
		return
	}
	// Replace, not add: a device must not be able to widen the policy.
	h.Set("Content-Security-Policy", webGUISandbox)
	// A sandboxed page cannot use cookies anyway; do not let a device
	// plant them on this origin.
	h.Del("Set-Cookie")
}

// stripOperatorCredentials removes what authenticated the operator to this
// API from a request about to be sent to a device.
func stripOperatorCredentials(req *http.Request) {
	req.Header.Del("Authorization")
	q := req.URL.Query()
	q.Del("token")
	req.URL.RawQuery = q.Encode()
}
