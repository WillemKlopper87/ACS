package bss

import (
	"errors"
	"fmt"
	"strings"

	"acs/internal/devices/adapters"
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

// actionTranslator turns one BSS action's business parameters into the
// canonical parameter writes to queue.
type actionTranslator func(params map[string]string, wg WalledGardenConfig, dataModelRoot string) ([]ParameterWrite, error)

// actionRegistry is the single place that lists every BSS action this
// system knows about (design S4) — previously a hardcoded switch inside
// Translate. Adding a fourth action means adding an entry here plus a
// test, the same cost as adding any other action/job type in this
// codebase (a CWMP or USP job type).
var actionRegistry = map[string]actionTranslator{
	"MODIFY_WIFI": func(params map[string]string, _ WalledGardenConfig, dataModelRoot string) ([]ParameterWrite, error) {
		return translateModifyWifi(params, dataModelRoot)
	},
	"SUSPEND": func(_ map[string]string, wg WalledGardenConfig, _ string) ([]ParameterWrite, error) {
		return translateWalledGarden(wg, wg.SuspendValue)
	},
	"ACTIVATE": func(_ map[string]string, wg WalledGardenConfig, _ string) ([]ParameterWrite, error) {
		return translateWalledGarden(wg, wg.ActiveValue)
	},
}

// Translate turns a BSS order's action + business parameters into the
// canonical parameter writes to queue (design doc v3 §6.2's canonical-name
// indirection, applied one layer up from vendor path resolution to
// business action — build plan §5.3), resolved to the actual device tree
// via internal/devices/adapters.ResolvePath and the target device's own
// discovered dataModelRoot. dataModelRoot may be "" (devices.DataModelRootUnknown)
// for actions, like SUSPEND/ACTIVATE, that don't need it at all.
func Translate(action string, params map[string]string, wg WalledGardenConfig, dataModelRoot string) ([]ParameterWrite, error) {
	return TranslateWithCapabilities(action, params, wg, dataModelRoot, nil)
}

// TranslateWithCapabilities is Translate with optional device-discovery
// evidence. Passing nil keeps the established compatibility fallback for a
// device that has not yet completed discovery. Passing a non-nil map makes
// canonical writes capability-aware: a BSS order is rejected before queuing
// if the device reported none of the known candidate paths as writable.
func TranslateWithCapabilities(action string, params map[string]string, wg WalledGardenConfig, dataModelRoot string, discovered map[string]bool) ([]ParameterWrite, error) {
	translator, ok := actionRegistry[action]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAction, action)
	}
	if action == "MODIFY_WIFI" {
		return translateModifyWifiWithCapabilities(params, dataModelRoot, discovered)
	}
	writes, err := translator(params, wg, dataModelRoot)
	if err != nil || discovered == nil {
		return writes, err
	}
	for _, write := range writes {
		writable, exists := discovered[write.Name]
		if !exists {
			return nil, fmt.Errorf("%w: parameter %q is not supported by the discovered device model", ErrUnsupportedAction, write.Name)
		}
		if !writable {
			return nil, fmt.Errorf("%w: parameter %q is read-only on the discovered device model", ErrUnsupportedAction, write.Name)
		}
	}
	return writes, nil
}

func translateWalledGarden(wg WalledGardenConfig, value string) ([]ParameterWrite, error) {
	if !wg.configured() {
		return nil, fmt.Errorf("%w: SUSPEND/ACTIVATE need ACS_WALLED_GARDEN_PARAMETER/ACS_WALLED_GARDEN_SUSPEND_VALUE/ACS_WALLED_GARDEN_ACTIVE_VALUE configured before they can be implemented safely (build plan §5.3 — no universal safe parameter across vendors, so this isn't guessed at)", ErrUnsupportedAction)
	}
	return []ParameterWrite{{Name: wg.Parameter, Value: value, Type: "string"}}, nil
}

func translateModifyWifi(params map[string]string, dataModelRoot string) ([]ParameterWrite, error) {
	return translateModifyWifiWithCapabilities(params, dataModelRoot, nil)
}

func translateModifyWifiWithCapabilities(params map[string]string, dataModelRoot string, discovered map[string]bool) ([]ParameterWrite, error) {
	var out []ParameterWrite
	if ssid := params["wifi_ssid"]; ssid != "" {
		path, err := adapters.ResolveWritablePath(dataModelRoot, adapters.WiFiSSID, discovered)
		if err != nil {
			return nil, fmt.Errorf("%w: Wi-Fi SSID: %v", ErrUnsupportedAction, err)
		}
		out = append(out, ParameterWrite{Name: path, Value: ssid, Type: "string"})
	}
	if pass := params["wifi_password"]; pass != "" {
		path, err := adapters.ResolveWritablePath(dataModelRoot, adapters.WiFiKeyPassphrase, discovered)
		if err != nil {
			return nil, fmt.Errorf("%w: Wi-Fi passphrase: %v", ErrUnsupportedAction, err)
		}
		out = append(out, ParameterWrite{Name: path, Value: pass, Type: "string"})
	}
	if raw := params["wifi_enabled"]; raw != "" {
		value, err := cwmpBoolean(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: wifi_enabled: %v", ErrInvalidParameters, err)
		}
		path, err := adapters.ResolveWritablePath(dataModelRoot, adapters.WiFiEnable, discovered)
		if err != nil {
			return nil, fmt.Errorf("%w: Wi-Fi enable: %v", ErrUnsupportedAction, err)
		}
		out = append(out, ParameterWrite{Name: path, Value: value, Type: "boolean"})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: MODIFY_WIFI requires at least one of wifi_ssid, wifi_password, wifi_enabled", ErrInvalidParameters)
	}
	return out, nil
}

// cwmpBoolean normalizes a BSS-supplied boolean string to TR-069's
// xsd:boolean wire form ("0"/"1"), accepting the common human-friendly
// spellings a JSON caller is likely to send rather than forcing them to
// already know CWMP's convention.
func cwmpBoolean(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true":
		return "1", nil
	case "0", "false":
		return "0", nil
	default:
		return "", fmt.Errorf("must be true/false or 1/0, got %q", raw)
	}
}
