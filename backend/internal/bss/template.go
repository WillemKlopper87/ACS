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

// wordFormBooleans is the set of common human-typed boolean spellings an
// operator might reasonably configure ACS_WALLED_GARDEN_SUSPEND_VALUE/
// ACS_WALLED_GARDEN_ACTIVE_VALUE with. Some CPE firmware (observed:
// Huawei's cwmpmng, documented independently by FreeACS's fix for the
// analogous NextLevel RPC parameter) strictly enforces TR-069's
// xsd:boolean numeric lexical form ("0"/"1") and silently rejects the
// word form. This matters here specifically because RenderSetParameterValues
// (internal/cwmp/rpc.go) tags every value xsi:type="xsd:string"
// regardless of the parameter's real declared type, so a misconfigured
// word-form value isn't caught by our own wire encoding — only the CPE's
// own data-model type coercion would reject it, invisibly, in
// production.
var wordFormBooleans = map[string]bool{
	"true": true, "false": true, "yes": true, "no": true, "on": true, "off": true,
}

// LooksLikeWordFormBoolean reports whether s is spelled like a boolean
// word ("true"/"false"/"yes"/"no"/"on"/"off", case-insensitive) rather
// than TR-069's "0"/"1" numeric lexical form. This is not a validation —
// a deployment's walled-garden parameter might genuinely be a
// string-typed one that legitimately takes the word "true" — just a
// signal worth a startup warning so an operator configuring a
// boolean-typed parameter notices before a device silently rejects it.
func LooksLikeWordFormBoolean(s string) bool {
	return wordFormBooleans[strings.ToLower(strings.TrimSpace(s))]
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
	translator, ok := actionRegistry[action]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAction, action)
	}
	return translator(params, wg, dataModelRoot)
}

func translateWalledGarden(wg WalledGardenConfig, value string) ([]ParameterWrite, error) {
	if !wg.configured() {
		return nil, fmt.Errorf("%w: SUSPEND/ACTIVATE need ACS_WALLED_GARDEN_PARAMETER/ACS_WALLED_GARDEN_SUSPEND_VALUE/ACS_WALLED_GARDEN_ACTIVE_VALUE configured before they can be implemented safely (build plan §5.3 — no universal safe parameter across vendors, so this isn't guessed at)", ErrUnsupportedAction)
	}
	return []ParameterWrite{{Name: wg.Parameter, Value: value, Type: "string"}}, nil
}

func translateModifyWifi(params map[string]string, dataModelRoot string) ([]ParameterWrite, error) {
	var out []ParameterWrite
	if ssid := params["wifi_ssid"]; ssid != "" {
		path, _ := adapters.ResolvePath(dataModelRoot, adapters.WiFiSSID)
		out = append(out, ParameterWrite{Name: path, Value: ssid, Type: "string"})
	}
	if pass := params["wifi_password"]; pass != "" {
		path, _ := adapters.ResolvePath(dataModelRoot, adapters.WiFiKeyPassphrase)
		out = append(out, ParameterWrite{Name: path, Value: pass, Type: "string"})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: MODIFY_WIFI requires at least one of wifi_ssid, wifi_password", ErrInvalidParameters)
	}
	return out, nil
}
