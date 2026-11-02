package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/jobs"
)

// Per-network Wi-Fi editing (one CPE, one SSID instance at a time):
// PUT /api/v1/devices/{id}/wifi/networks/{instance}. Unlike the old "set
// SSID" box, which renamed every primary network at once, an edit names
// the network(s) it changes, carries name, passphrase, security,
// broadcast and on/off together in one SetParameterValues, and is refused
// when it would leave the CPE with no Wi-Fi or an open network that was
// not asked for. "Adding" a network is enabling a spare SSID instance the
// device already has (the Nokia FastMile ships eight), with a name and a
// passphrase.

// wifiNetworkEdit is the request body; absent fields stay as they are.
type wifiNetworkEdit struct {
	Name         *string `json:"name"`
	Passphrase   *string `json:"passphrase"`
	SecurityMode *string `json:"security_mode"`
	Enabled      *bool   `json:"enabled"`
	Advertised   *bool   `json:"advertised"`
	// AlsoInstances applies the same change to more networks, e.g. the
	// same-named network on the other band.
	AlsoInstances []int `json:"also_instances"`
	// AllowOpen confirms a network with no security ("None").
	AllowOpen bool `json:"allow_open"`
}

// resolveWiFiView loads the device's Wi-Fi network view, or explains why
// there is none (unsupported is true then).
func (h *handler) resolveWiFiView(r *http.Request, device *devices.Device) (view adapters.WiFiNetworks, reason string, err error) {
	if device.DataModelRoot == devices.DataModelRootIGD1 {
		return view, "TR-098 (InternetGatewayDevice) Wi-Fi networks are not resolved per instance yet", nil
	}
	discovered, err := h.params.GetNames(r.Context(), device.ID)
	if err != nil {
		return view, "", err
	}
	if discovered == nil || len(discovered.Names) == 0 {
		return view, "run parameter discovery first", nil
	}
	cache, err := h.params.Get(r.Context(), device.ID)
	if err != nil {
		return view, "", err
	}
	return adapters.ResolveWiFiNetworks(discovered.Names, cache), "", nil
}

func validWiFiName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > 32 {
		return fmt.Errorf("the network name must be 1 to 32 bytes")
	}
	return nil
}

// validPassphrase accepts a WPA passphrase: 8 to 63 printable ASCII
// characters, or exactly 64 hexadecimal digits (a raw key).
func validPassphrase(p string) error {
	if len(p) == 64 {
		for _, c := range p {
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return fmt.Errorf("a 64-character passphrase must be hexadecimal")
			}
		}
		return nil
	}
	if len(p) < 8 || len(p) > 63 {
		return fmt.Errorf("the passphrase must be 8 to 63 characters")
	}
	for _, c := range p {
		if c < 0x20 || c > 0x7e {
			return fmt.Errorf("the passphrase may contain only printable ASCII characters")
		}
	}
	return nil
}

func isOpenMode(mode string) bool { return strings.EqualFold(strings.TrimSpace(mode), "None") }

// updateWiFiNetwork is PUT /api/v1/devices/{id}/wifi/networks/{instance}.
func (h *handler) updateWiFiNetwork(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	instance, err := strconv.Atoi(r.PathValue("instance"))
	if err != nil || instance < 1 {
		http.Error(w, "instance must be a positive SSID instance number", http.StatusBadRequest)
		return
	}
	var req wifiNetworkEdit
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Name == nil && req.Passphrase == nil && req.SecurityMode == nil && req.Enabled == nil && req.Advertised == nil {
		http.Error(w, "nothing to change: send at least one of name, passphrase, security_mode, enabled, advertised", http.StatusBadRequest)
		return
	}
	if req.Name != nil {
		if err := validWiFiName(*req.Name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Passphrase != nil {
		if err := validPassphrase(*req.Passphrase); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	view, reason, err := h.resolveWiFiView(r, device)
	if err != nil {
		h.logger.Error("failed to resolve Wi-Fi networks", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if reason != "" {
		http.Error(w, reason, http.StatusConflict)
		return
	}
	if !view.Complete {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":         "this device's Wi-Fi settings are not fully read yet; read refresh_paths, then retry after its next check-in",
			"refresh_paths": view.RefreshPaths,
		})
		return
	}
	byInstance := map[int]adapters.WiFiNetwork{}
	for _, n := range view.Networks {
		byInstance[n.Instance] = n
	}
	targets := []int{instance}
	for _, i := range req.AlsoInstances {
		if !slices.Contains(targets, i) {
			targets = append(targets, i)
		}
	}

	var writes []jobs.ParameterWrite
	add := func(n adapters.WiFiNetwork, field, path string, writable bool, value, typ string) error {
		if path == "" || !writable {
			return fmt.Errorf("network %d: %s is not writable on this device", n.Instance, field)
		}
		writes = append(writes, jobs.ParameterWrite{Name: path, Value: value, Type: typ})
		return nil
	}
	cwmpBool := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	disconnects := false
	for _, inst := range targets {
		n, ok := byInstance[inst]
		if !ok {
			http.Error(w, fmt.Sprintf("network %d does not exist on this device", inst), http.StatusNotFound)
			return
		}
		if n.AccessPoint == "" {
			http.Error(w, fmt.Sprintf("network %d is not served by an access point (a mesh backhaul or unused SSID) and cannot be edited here", inst), http.StatusConflict)
			return
		}
		wasOn := n.Enabled == nil || *n.Enabled
		on := wasOn
		if req.Enabled != nil {
			on = *req.Enabled
		}
		mode := n.SecurityMode
		if req.SecurityMode != nil {
			mode = strings.TrimSpace(*req.SecurityMode)
			if len(n.ModesSupported) > 0 && !slices.Contains(n.ModesSupported, mode) {
				http.Error(w, fmt.Sprintf("network %d supports security modes %s, not %q", inst, strings.Join(n.ModesSupported, ", "), mode), http.StatusBadRequest)
				return
			}
		}
		if on && isOpenMode(mode) && !req.AllowOpen {
			http.Error(w, fmt.Sprintf("network %d would be open (no security); choose a WPA mode, or confirm with allow_open", inst), http.StatusBadRequest)
			return
		}
		if req.Passphrase != nil && isOpenMode(mode) {
			http.Error(w, fmt.Sprintf("network %d has no security, so a passphrase does not apply; choose a WPA mode", inst), http.StatusBadRequest)
			return
		}
		if req.SecurityMode != nil && isOpenMode(n.SecurityMode) && !isOpenMode(mode) && req.Passphrase == nil {
			http.Error(w, fmt.Sprintf("network %d is being secured: set a passphrase with the new mode", inst), http.StatusBadRequest)
			return
		}
		if !wasOn && on && req.Passphrase == nil && !isOpenMode(mode) {
			http.Error(w, fmt.Sprintf("network %d is being turned on: set its passphrase too (the current one is not known to the ACS)", inst), http.StatusBadRequest)
			return
		}

		if req.Name != nil {
			if err := add(n, "the name", n.NamePath, n.NameWritable, *req.Name, "xsd:string"); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
		}
		if req.SecurityMode != nil {
			if err := add(n, "the security mode", n.SecurityModePath, n.SecurityModeWritable, mode, "xsd:string"); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
		}
		if req.Passphrase != nil {
			if err := add(n, "the passphrase", n.PassphrasePath, n.PassphraseWrite, *req.Passphrase, "xsd:string"); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
		}
		if req.Advertised != nil {
			if err := add(n, "broadcast", n.AdvertisedPath, n.AdvertisedWritable, cwmpBool(*req.Advertised), "xsd:boolean"); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
		}
		if req.Enabled != nil {
			if err := add(n, "on/off", n.EnablePath, n.EnableWritable, cwmpBool(on), "xsd:boolean"); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			// The AccessPoint follows the SSID when the device exposes it.
			if n.APEnablePath != "" && n.APEnableWritable {
				writes = append(writes, jobs.ParameterWrite{Name: n.APEnablePath, Value: cwmpBool(on), Type: "xsd:boolean"})
			}
		}
		if wasOn && (req.Name != nil || req.Passphrase != nil || req.SecurityMode != nil || !on) {
			disconnects = true
		}
	}

	// Never switch off every Wi-Fi network in one change.
	if req.Enabled != nil && !*req.Enabled {
		stillOn := 0
		for _, n := range view.Networks {
			if n.AccessPoint != "" && (n.Enabled == nil || *n.Enabled) && !slices.Contains(targets, n.Instance) {
				stillOn++
			}
		}
		if stillOn == 0 {
			http.Error(w, "this would turn off every Wi-Fi network on the device; keep at least one on", http.StatusConflict)
			return
		}
	}

	payload := jobs.SetParameterPayload{Parameters: writes, Verify: true}
	if refuseProtectedWrite(w, writeNames(payload)...) {
		return
	}
	job, err := h.jobs.Create(r.Context(), device.ID, jobs.TypeSetParameter, payload, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue Wi-Fi change", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	details := map[string]any{"instances": targets, "command_key": job.CommandKey, "passphrase_changed": req.Passphrase != nil}
	if req.Name != nil {
		details["name"] = *req.Name
	}
	if req.SecurityMode != nil {
		details["security_mode"] = *req.SecurityMode
	}
	if req.Enabled != nil {
		details["enabled"] = *req.Enabled
	}
	if req.Advertised != nil {
		details["advertised"] = *req.Advertised
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), device.ID, "WiFiNetworkChanged", details); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	paths := make([]string, len(writes))
	for i, wr := range writes {
		paths[i] = wr.Name
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_key": job.CommandKey,
		"status":      job.Status,
		"instances":   targets,
		"writes":      paths,
		"disconnects": disconnects,
	})
}

// wifiClientCounts returns connected clients per SSID instance from the
// latest Wi-Fi insights sample (at most 30 minutes old), or nil.
func (h *handler) wifiClientCounts(r *http.Request, deviceID string) (map[string]int, *time.Time) {
	if h.wifiInsights == nil {
		return nil, nil
	}
	counts, at, err := h.wifiInsights.ConnectedBySSID(r.Context(), deviceID, 30*time.Minute)
	if err != nil {
		h.logger.Warn("failed to count Wi-Fi clients", "err", err, "device_id", deviceID)
		return nil, nil
	}
	if counts == nil {
		return nil, nil
	}
	out := make(map[string]int, len(counts))
	for k, v := range counts {
		out[strconv.Itoa(k)] = v
	}
	return out, &at
}
