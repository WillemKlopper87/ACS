// canonical.go implements design doc v3 §6.2/§6.3's "canonical parameter
// mapping" — the piece catalog.go's own doc comment flagged as "later-phase
// work" back when only the vendor catalogs existed. Every write/read path
// in cmd/acs, cmd/api, and internal/bss that used to hardcode a "Device."
// (TR-181) literal now resolves through here, branching on the device's
// own discovered devices.DataModelRoot (parameter discovery) instead of
// assuming TR-181 unconditionally — closing the gap
// tr069-acs-build-plan.md §10 flagged: "a genuine IGD:1-only device's
// writes would go to the wrong tree."
//
// Scope, deliberately: only parameters this codebase already writes/reads
// by a hardcoded literal, and only where the TR-098 correspondence is
// unambiguous and spec-documented — not a general-purpose arbitrary-path
// translator. An operator-supplied path (e.g. typed into a console
// SET_PARAMETER action) is left exactly as typed; the operator already
// knows their own device's real tree from parameter discovery, and
// silently rewriting a path they explicitly chose would be surprising,
// not helpful. That's the same boundary the per-vendor canonical-parameter
// registry gap (build plan §10) draws — this closes the narrower, buildable
// slice of it: the paths *this codebase* already assumes.
package adapters

import "acs/internal/devices"

// CanonicalParameter names a parameter this codebase needs to read or
// write independent of which data model a device actually speaks —
// design doc v3 §6.3's CanonicalParameter registry.
type CanonicalParameter string

const (
	DeviceInfoSoftwareVersion             CanonicalParameter = "device_info.software_version"
	ManagementServerConnectionRequestURL  CanonicalParameter = "management_server.connection_request_url"
	ManagementServerConnectionRequestUser CanonicalParameter = "management_server.connection_request_username"
	ManagementServerConnectionRequestPass CanonicalParameter = "management_server.connection_request_password"
	ManagementServerUsername              CanonicalParameter = "management_server.username" // CPE->ACS Digest identity
	ManagementServerPassword              CanonicalParameter = "management_server.password"
	WiFiSSID                              CanonicalParameter = "wifi.ssid"
	WiFiKeyPassphrase                     CanonicalParameter = "wifi.key_passphrase"
)

// device2Paths is the TR-181 (Device:2) resolution — matches every literal
// already hardcoded elsewhere in this codebase before this file existed,
// so DataModelRootDevice2/DataModelRootUnknown behavior is unchanged.
var device2Paths = map[CanonicalParameter]string{
	DeviceInfoSoftwareVersion:             "Device.DeviceInfo.SoftwareVersion",
	ManagementServerConnectionRequestURL:  "Device.ManagementServer.ConnectionRequestURL",
	ManagementServerConnectionRequestUser: "Device.ManagementServer.ConnectionRequestUsername",
	ManagementServerConnectionRequestPass: "Device.ManagementServer.ConnectionRequestPassword",
	ManagementServerUsername:              "Device.ManagementServer.Username",
	ManagementServerPassword:              "Device.ManagementServer.Password",
	WiFiSSID:                              "Device.WiFi.SSID.1.SSID",
	WiFiKeyPassphrase:                     "Device.WiFi.AccessPoint.1.Security.KeyPassphrase",
}

// igd1Paths mirrors device2Paths for TR-098 (InternetGatewayDevice:1).
// The WiFi entries assume LANDevice.1/WLANConfiguration.1 — the standard
// single-primary-radio convention essentially every real IGD:1 CPE uses
// (TR-098 doesn't define a "primary" instance the way TR-181's flatter
// WiFi.SSID.{i} implicitly does via instance 1; "1" is the de facto
// universal convention for the first/primary radio, not a spec guarantee).
// A genuinely multi-radio IGD:1 device needs parameter discovery's actual
// instance list — this deliberately doesn't attempt to guess beyond the
// common case, the same honesty boundary the per-vendor registry gap
// (build plan §10) already draws elsewhere.
var igd1Paths = map[CanonicalParameter]string{
	DeviceInfoSoftwareVersion:             "InternetGatewayDevice.DeviceInfo.SoftwareVersion",
	ManagementServerConnectionRequestURL:  "InternetGatewayDevice.ManagementServer.ConnectionRequestURL",
	ManagementServerConnectionRequestUser: "InternetGatewayDevice.ManagementServer.ConnectionRequestUsername",
	ManagementServerConnectionRequestPass: "InternetGatewayDevice.ManagementServer.ConnectionRequestPassword",
	ManagementServerUsername:              "InternetGatewayDevice.ManagementServer.Username",
	ManagementServerPassword:              "InternetGatewayDevice.ManagementServer.Password",
	WiFiSSID:                              "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.SSID",
	WiFiKeyPassphrase:                     "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.KeyPassphrase",
}

// ResolvePath maps a CanonicalParameter to the actual path for the given
// data_model_root (design doc v3 §6.3's resolve_path). Falls back to the
// TR-181 (device2Paths) entry for DataModelRootUnknown/DataModelRootDevice2
// — matching this codebase's existing "TR-181 first" convention (build
// plan §3) everywhere else, so this only changes behavior once discovery
// has actually confirmed IGD1.
func ResolvePath(root string, p CanonicalParameter) (string, bool) {
	if root == devices.DataModelRootIGD1 {
		path, ok := igd1Paths[p]
		return path, ok
	}
	path, ok := device2Paths[p]
	return path, ok
}

// Diagnostic kinds for DiagnosticsPrefix.
const (
	DiagnosticPing       = "ping"
	DiagnosticTraceroute = "traceroute"
)

// DiagnosticsPrefix returns the root object prefix for the ping or
// traceroute diagnostic, branching on data_model_root. The leaf parameter
// names under it (Host, NumberOfRepetitions, DiagnosticsState,
// SuccessCount, ...) are identical between TR-181 and TR-098 (TR-069 kept
// diagnostic leaf names stable across data models) — only the object's
// location in the tree differs: TR-181 nests diagnostics under
// IP.Diagnostics.*, TR-098 keeps them as top-level *Diagnostics objects.
func DiagnosticsPrefix(root, kind string) string {
	igd1 := root == devices.DataModelRootIGD1
	switch {
	case igd1 && kind == DiagnosticTraceroute:
		return "InternetGatewayDevice.TraceRouteDiagnostics."
	case igd1:
		return "InternetGatewayDevice.IPPingDiagnostics."
	case kind == DiagnosticTraceroute:
		return "Device.IP.Diagnostics.TraceRoute."
	default:
		return "Device.IP.Diagnostics.IPPing."
	}
}

// WiFiAssociatedDevicesPrefix returns the partial path to sweep
// (GetParameterNames/GetParameterValues over a whole subtree) for a
// device's associated WiFi clients — the "refresh WiFi clients"
// convenience endpoint. No trailing instance number on WLANConfiguration
// for IGD1, deliberately: the TR-181 sweep (Device.WiFi.AccessPoint.)
// already covers every AccessPoint instance's AssociatedDevice table in
// one partial-path read (its own doc comment: "every SSID's
// AssociatedDevice table included"), so the IGD1 equivalent sweeps every
// WLANConfiguration instance under the primary LANDevice the same way,
// not just the first. Same LANDevice.1 caveat as WiFiSSID above.
func WiFiAssociatedDevicesPrefix(root string) string {
	if root == devices.DataModelRootIGD1 {
		return "InternetGatewayDevice.LANDevice.1.WLANConfiguration."
	}
	return "Device.WiFi.AccessPoint."
}
