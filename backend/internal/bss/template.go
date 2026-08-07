package bss

import (
	"errors"
	"fmt"
)

// ParameterWrite is one parameter the internal ACS REST API's PUT
// .../parameters endpoint expects. Deliberately duplicated rather than
// importing internal/jobs.ParameterWrite — bssadapter only ever talks to
// that API over HTTP (build plan §5.1), so it shouldn't share a Go type
// with the process on the other side of that boundary.
type ParameterWrite struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

var (
	// ErrUnsupportedAction covers both "no such action" and "an action
	// that exists in the BSS contract but isn't safe to implement yet."
	ErrUnsupportedAction = errors.New("unsupported or not-yet-implemented BSS action")
	// ErrInvalidParameters means the action is known but the caller's
	// Parameters map didn't contain what the template needs.
	ErrInvalidParameters = errors.New("order parameters do not satisfy the requested action")
)

// WalledGardenConfig is the per-deployment answer to build plan §5.3's
// open design question. The reference internal_bss_adapter.go draft's
// SUSPEND template set Device.IP.Interface.1.Enable=false — the same WAN
// interface CWMP itself needs to reach the device, risking the session
// before SetParameterValuesResponse is even confirmed, and possibly
// stranding the device unreachable for its own follow-up ACTIVATE order
// (design doc v3 §19.5: don't rely on a single reachability path). There
// is no universal safe parameter, though — what actually isolates a
// device without touching its WAN interface (a firewall/NAT rule object,
// a captive-portal toggle, a dedicated vendor "walled garden" parameter)
// depends on the CPE vendor, and picking one here would be guessing at a
// real product/network decision instead of building it. So this is
// supplied by the deployer — ACS_WALLED_GARDEN_PARAMETER /
// ACS_WALLED_GARDEN_SUSPEND_VALUE / ACS_WALLED_GARDEN_ACTIVE_VALUE — the
// same "off unless configured, loud warning when it isn't" convention
// every other credential/feature gate in this codebase already uses
// (Digest auth, JWT auth, mTLS, Connection Request credentials). Left
// unconfigured, SUSPEND/ACTIVATE stay blocked exactly as before.
type WalledGardenConfig struct {
	Parameter    string
	SuspendValue string
	ActiveValue  string
}

func (c WalledGardenConfig) configured() bool {
	return c.Parameter != "" && c.SuspendValue != "" && c.ActiveValue != ""
}

// Translate turns a BSS order's action + business parameters into the
// canonical TR-181 parameter writes to queue (design doc v3 §6.2's
// canonical-name indirection, applied one layer up from vendor path
// resolution to business action — build plan §5.3).
func Translate(action string, params map[string]string, wg WalledGardenConfig) ([]ParameterWrite, error) {
	switch action {
	case "MODIFY_WIFI":
		return translateModifyWifi(params)
	case "SUSPEND":
		return translateWalledGarden(wg, wg.SuspendValue)
	case "ACTIVATE":
		return translateWalledGarden(wg, wg.ActiveValue)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAction, action)
	}
}

func translateWalledGarden(wg WalledGardenConfig, value string) ([]ParameterWrite, error) {
	if !wg.configured() {
		return nil, fmt.Errorf("%w: SUSPEND/ACTIVATE need ACS_WALLED_GARDEN_PARAMETER/ACS_WALLED_GARDEN_SUSPEND_VALUE/ACS_WALLED_GARDEN_ACTIVE_VALUE configured before they can be implemented safely (build plan §5.3 — no universal safe parameter across vendors, so this isn't guessed at)", ErrUnsupportedAction)
	}
	return []ParameterWrite{{Name: wg.Parameter, Value: value, Type: "string"}}, nil
}

func translateModifyWifi(params map[string]string) ([]ParameterWrite, error) {
	var out []ParameterWrite
	if ssid := params["wifi_ssid"]; ssid != "" {
		out = append(out, ParameterWrite{Name: "Device.WiFi.SSID.1.SSID", Value: ssid, Type: "string"})
	}
	if pass := params["wifi_password"]; pass != "" {
		out = append(out, ParameterWrite{Name: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", Value: pass, Type: "string"})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: MODIFY_WIFI requires at least one of wifi_ssid, wifi_password", ErrInvalidParameters)
	}
	return out, nil
}
