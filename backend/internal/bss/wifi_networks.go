package bss

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// WiFiNetworkTarget is the subset of the ACS's resolved Wi-Fi network
// (GET /api/v1/devices/{id}/wifi/networks) the BSS adapter writes to.
type WiFiNetworkTarget struct {
	Instance           int    `json:"instance"`
	Band               string `json:"band"`
	NamePath           string `json:"name_path"`
	EnablePath         string `json:"enable_path"`
	PassphrasePath     string `json:"passphrase_path"`
	NameWritable       bool   `json:"name_writable"`
	EnableWritable     bool   `json:"enable_writable"`
	PassphraseWritable bool   `json:"passphrase_writable"`
}

// WiFiNetworksView is the ACS's resolved Wi-Fi view for one device.
type WiFiNetworksView struct {
	Supported    bool                `json:"supported"`
	Reason       string              `json:"reason"`
	Complete     bool                `json:"complete"`
	Primary      []WiFiNetworkTarget `json:"primary"`
	RefreshPaths []string            `json:"refresh_paths"`
}

// GetWiFiNetworks fetches the device's resolved Wi-Fi networks.
func (c *ACSClient) GetWiFiNetworks(ctx context.Context, deviceID string) (*WiFiNetworksView, error) {
	url := fmt.Sprintf("%s/api/v1/devices/%s/wifi/networks", c.baseURL, deviceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build wifi networks request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrACSUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrDeviceLookupNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status from ACS: %d", resp.StatusCode)
	}
	var out WiFiNetworksView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode wifi networks response: %w", err)
	}
	return &out, nil
}

// TranslateModifyWifiNetworks turns a MODIFY_WIFI order into writes on every
// primary (fronthaul) network of a TR-181 device — one SSID per band on a
// dual-band CPE — instead of the fixed Device.WiFi.SSID.1 that renamed only
// one band (or a guest network) on multi-SSID CPEs such as the Nokia
// FastMile. Guest and unbound (e.g. mesh backhaul) SSIDs are never written.
func TranslateModifyWifiNetworks(params map[string]string, view *WiFiNetworksView) ([]ParameterWrite, error) {
	if view == nil || !view.Supported {
		reason := "no Wi-Fi networks resolved"
		if view != nil && view.Reason != "" {
			reason = view.Reason
		}
		return nil, fmt.Errorf("%w: MODIFY_WIFI: %s", ErrUnsupportedAction, reason)
	}
	if !view.Complete {
		return nil, fmt.Errorf("%w: MODIFY_WIFI: the device's Wi-Fi roles are not known yet (values not read: %v); retry after its next check-in", ErrUnsupportedAction, view.RefreshPaths)
	}
	if len(view.Primary) == 0 {
		return nil, fmt.Errorf("%w: MODIFY_WIFI: the device has no primary (non-guest) Wi-Fi network", ErrUnsupportedAction)
	}
	var out []ParameterWrite
	for _, n := range view.Primary {
		if ssid := params["wifi_ssid"]; ssid != "" {
			if n.NamePath == "" || !n.NameWritable {
				return nil, fmt.Errorf("%w: MODIFY_WIFI: SSID name of network %d is not writable", ErrUnsupportedAction, n.Instance)
			}
			out = append(out, ParameterWrite{Name: n.NamePath, Value: ssid, Type: "xsd:string"})
		}
		if pass := params["wifi_password"]; pass != "" {
			if n.PassphrasePath == "" || !n.PassphraseWritable {
				return nil, fmt.Errorf("%w: MODIFY_WIFI: passphrase of network %d is not writable", ErrUnsupportedAction, n.Instance)
			}
			out = append(out, ParameterWrite{Name: n.PassphrasePath, Value: pass, Type: "xsd:string"})
		}
		if raw := params["wifi_enabled"]; raw != "" {
			value, err := cwmpBoolean(raw)
			if err != nil {
				return nil, fmt.Errorf("%w: wifi_enabled: %v", ErrInvalidParameters, err)
			}
			if n.EnablePath == "" || !n.EnableWritable {
				return nil, fmt.Errorf("%w: MODIFY_WIFI: enable flag of network %d is not writable", ErrUnsupportedAction, n.Instance)
			}
			out = append(out, ParameterWrite{Name: n.EnablePath, Value: value, Type: "xsd:boolean"})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: MODIFY_WIFI requires at least one of wifi_ssid, wifi_password, wifi_enabled", ErrInvalidParameters)
	}
	return out, nil
}
