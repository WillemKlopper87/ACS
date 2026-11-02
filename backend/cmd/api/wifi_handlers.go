package main

import (
	"net/http"
	"time"

	"acs/internal/devices/adapters"
	"acs/internal/wifiinsights"
)

// getWiFiNetworks resolves the device's TR-181 Wi-Fi networks (SSID +
// AccessPoint + Radio, role primary/guest/unbound) from discovery and the
// cached values, instead of assuming Device.WiFi.SSID.1. Never returns
// passphrases. read_paths are the non-secret values to GET to refresh the
// view; when complete is false, refresh_paths must be read before writing.
func (h *handler) getWiFiNetworks(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	view, reason, err := h.resolveWiFiView(r, device)
	if err != nil {
		h.logger.Error("failed to resolve Wi-Fi networks", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if reason != "" {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false, "reason": reason, "networks": []any{}})
		return
	}
	discovered, err := h.params.GetNames(r.Context(), device.ID)
	if err != nil {
		h.logger.Error("failed to read discovered parameter names", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	clients, clientsAt := h.wifiClientCounts(r, device.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"supported":     len(view.Networks) > 0,
		"complete":      view.Complete,
		"networks":      view.Networks,
		"primary":       adapters.PrimaryWiFiNetworks(view),
		"refresh_paths": view.RefreshPaths,
		"read_paths":    adapters.WiFiNetworkReadPaths(discovered.Names),
		// Connected clients per SSID instance from the latest Wi-Fi
		// insights sample; absent when there is no recent sample.
		"clients_by_instance": clients,
		"clients_sampled_at":  clientsAt,
	})
}

// getWiFiInsights serves the controller-style overview (bandwidth, client
// counts, top SSIDs/clients, new/returning clients, vendors, inferred OS)
// for ?window=2h|1d|1w|1m (default 2h). Data exists only from when cmd/acs
// started sampling the device (ACS_WIFI_INSIGHTS_INTERVAL); there is no
// backfill because the CPE keeps no history.
func (h *handler) getWiFiInsights(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	name := r.URL.Query().Get("window")
	if name == "" {
		name = "2h"
	}
	window, ok := wifiinsights.Windows[name]
	if !ok {
		http.Error(w, "window must be one of 2h, 1d, 1w, 1m", http.StatusBadRequest)
		return
	}
	overview, err := h.wifiInsights.Overview(r.Context(), device.ID, window, time.Now().UTC())
	if err != nil {
		h.logger.Error("failed to build Wi-Fi insights", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}
