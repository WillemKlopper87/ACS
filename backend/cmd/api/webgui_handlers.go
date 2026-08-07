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
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"

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
	if _, err := h.devices.Get(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to get device", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var req setWebGUIConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if _, err := url.ParseRequestURI(req.BaseURL); err != nil {
		http.Error(w, "base_url must be a valid absolute URL", http.StatusBadRequest)
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

	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		req.URL.Path = "/" + r.PathValue("path")
		originalDirector(req)
		if cfg.Username != "" {
			req.SetBasicAuth(cfg.Username, cfg.Password)
		}
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.logger.Warn("webgui proxy request failed — device likely unreachable", "err", err, "device_id", id, "base_url", cfg.BaseURL)
		http.Error(w, "device web GUI unreachable: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}
