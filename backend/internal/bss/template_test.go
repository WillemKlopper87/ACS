package bss

import (
	"errors"
	"testing"

	"acs/internal/devices"
)

func TestTranslateModifyWifi(t *testing.T) {
	params, err := Translate("MODIFY_WIFI", map[string]string{
		"wifi_ssid":     "Smith_Family_5G",
		"wifi_password": "SuperSecretPassword123",
	}, WalledGardenConfig{}, devices.DataModelRootDevice2)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(params) != 2 {
		t.Fatalf("params len = %d, want 2: %+v", len(params), params)
	}

	want := map[string]string{
		"Device.WiFi.SSID.1.SSID":                          "Smith_Family_5G",
		"Device.WiFi.AccessPoint.1.Security.KeyPassphrase": "SuperSecretPassword123",
	}
	for _, p := range params {
		v, ok := want[p.Name]
		if !ok {
			t.Errorf("unexpected parameter %q", p.Name)
			continue
		}
		if p.Value != v {
			t.Errorf("Name %q Value = %q, want %q", p.Name, p.Value, v)
		}
		if p.Type != "string" {
			t.Errorf("Name %q Type = %q, want string", p.Name, p.Type)
		}
	}
}

// TestTranslateModifyWifiIGD1 is the build plan §10 gap this pass closes:
// a device confirmed to speak TR-098 (IGD:1) must get IGD:1-shaped writes,
// not TR-181 paths that don't exist on its tree.
func TestTranslateModifyWifiIGD1(t *testing.T) {
	params, err := Translate("MODIFY_WIFI", map[string]string{
		"wifi_ssid":     "Legacy_Gateway",
		"wifi_password": "OldButGold123",
	}, WalledGardenConfig{}, devices.DataModelRootIGD1)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}

	want := map[string]string{
		"InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.SSID":                         "Legacy_Gateway",
		"InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.PreSharedKey.1.KeyPassphrase": "OldButGold123",
	}
	if len(params) != len(want) {
		t.Fatalf("params len = %d, want %d: %+v", len(params), len(want), params)
	}
	for _, p := range params {
		v, ok := want[p.Name]
		if !ok {
			t.Errorf("unexpected parameter %q", p.Name)
			continue
		}
		if p.Value != v {
			t.Errorf("Name %q Value = %q, want %q", p.Name, p.Value, v)
		}
	}
}

func TestTranslateModifyWifiUsesDiscoveredWritableAlternate(t *testing.T) {
	const primary = "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.PreSharedKey.1.KeyPassphrase"
	const alternate = "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.KeyPassphrase"

	params, err := TranslateWithCapabilities("MODIFY_WIFI", map[string]string{"wifi_password": "OldButGold123"}, WalledGardenConfig{}, devices.DataModelRootIGD1, map[string]bool{
		primary:   false,
		alternate: true,
	})
	if err != nil {
		t.Fatalf("TranslateWithCapabilities: %v", err)
	}
	if len(params) != 1 || params[0].Name != alternate {
		t.Fatalf("params = %+v, want writable alternate %q", params, alternate)
	}
}

func TestTranslateModifyWifiRejectsKnownUnsupportedPath(t *testing.T) {
	_, err := TranslateWithCapabilities("MODIFY_WIFI", map[string]string{"wifi_password": "x"}, WalledGardenConfig{}, devices.DataModelRootIGD1, map[string]bool{})
	if !errors.Is(err, ErrUnsupportedAction) {
		t.Errorf("err = %v, want ErrUnsupportedAction", err)
	}
}

func TestTranslateModifyWifiPartial(t *testing.T) {
	params, err := Translate("MODIFY_WIFI", map[string]string{"wifi_ssid": "OnlySSID"}, WalledGardenConfig{}, devices.DataModelRootDevice2)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(params) != 1 || params[0].Name != "Device.WiFi.SSID.1.SSID" {
		t.Errorf("params = %+v, want just the SSID write", params)
	}
}

// TestTranslateModifyWifiEnabled covers the wifi.enable canonical
// parameter widening MODIFY_WIFI to more than SSID/passphrase, resolved
// through the same capability-aware ResolveWritablePath path.
func TestTranslateModifyWifiEnabled(t *testing.T) {
	params, err := Translate("MODIFY_WIFI", map[string]string{"wifi_enabled": "true"}, WalledGardenConfig{}, devices.DataModelRootDevice2)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(params) != 1 || params[0].Name != "Device.WiFi.SSID.1.Enable" || params[0].Value != "1" || params[0].Type != "boolean" {
		t.Fatalf("params = %+v, want a single boolean Enable write of \"1\"", params)
	}
}

// TestTranslateModifyWifiEnabledFalse proves the human-friendly "false"
// spelling normalizes to CWMP's xsd:boolean wire form "0", not the literal
// string "false" (which most CPEs would reject or silently misinterpret).
func TestTranslateModifyWifiEnabledFalse(t *testing.T) {
	params, err := Translate("MODIFY_WIFI", map[string]string{"wifi_enabled": "false"}, WalledGardenConfig{}, devices.DataModelRootDevice2)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(params) != 1 || params[0].Value != "0" {
		t.Fatalf("params = %+v, want Enable=\"0\"", params)
	}
}

// TestTranslateModifyWifiEnabledIGD1 proves the TR-098 root resolves to
// the WLANConfiguration Enable leaf, mirroring how SSID/passphrase already
// branch on dataModelRoot.
func TestTranslateModifyWifiEnabledIGD1(t *testing.T) {
	params, err := Translate("MODIFY_WIFI", map[string]string{"wifi_enabled": "1"}, WalledGardenConfig{}, devices.DataModelRootIGD1)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(params) != 1 || params[0].Name != "InternetGatewayDevice.LANDevice.1.WLANConfiguration.1.Enable" {
		t.Fatalf("params = %+v, want the IGD1 Enable leaf", params)
	}
}

// TestTranslateModifyWifiEnabledRejectsGarbage proves an unrecognized
// value is rejected before ever reaching the device, rather than being
// forwarded as a literal string a CPE would silently misinterpret.
func TestTranslateModifyWifiEnabledRejectsGarbage(t *testing.T) {
	_, err := Translate("MODIFY_WIFI", map[string]string{"wifi_enabled": "yes please"}, WalledGardenConfig{}, devices.DataModelRootDevice2)
	if !errors.Is(err, ErrInvalidParameters) {
		t.Errorf("err = %v, want ErrInvalidParameters", err)
	}
}

// TestTranslateModifyWifiEnabledCombinesWithOtherFields proves
// wifi_enabled composes with wifi_ssid/wifi_password in one order rather
// than being mutually exclusive.
func TestTranslateModifyWifiEnabledCombinesWithOtherFields(t *testing.T) {
	params, err := Translate("MODIFY_WIFI", map[string]string{
		"wifi_ssid":    "Combined",
		"wifi_enabled": "true",
	}, WalledGardenConfig{}, devices.DataModelRootDevice2)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(params) != 2 {
		t.Fatalf("params = %+v, want both the SSID and Enable writes", params)
	}
}

// TestTranslateModifyWifiEnabledRespectsCapabilityGate proves
// wifi_enabled is rejected, not silently guessed at, when the device's own
// discovery evidence says the Enable leaf isn't writable -- the same
// capability gate wifi_ssid/wifi_password already go through.
func TestTranslateModifyWifiEnabledRespectsCapabilityGate(t *testing.T) {
	_, err := TranslateWithCapabilities("MODIFY_WIFI", map[string]string{"wifi_enabled": "true"}, WalledGardenConfig{}, devices.DataModelRootDevice2, map[string]bool{
		"Device.WiFi.SSID.1.Enable": false,
	})
	if !errors.Is(err, ErrUnsupportedAction) {
		t.Errorf("err = %v, want ErrUnsupportedAction", err)
	}
}

func TestTranslateModifyWifiRejectsEmptyParams(t *testing.T) {
	_, err := Translate("MODIFY_WIFI", map[string]string{}, WalledGardenConfig{}, devices.DataModelRootDevice2)
	if !errors.Is(err, ErrInvalidParameters) {
		t.Errorf("err = %v, want ErrInvalidParameters", err)
	}
}

func TestTranslateSuspendActivateUnconfigured(t *testing.T) {
	for _, action := range []string{"SUSPEND", "ACTIVATE"} {
		_, err := Translate(action, map[string]string{}, WalledGardenConfig{}, "")
		if !errors.Is(err, ErrUnsupportedAction) {
			t.Errorf("action %q: err = %v, want ErrUnsupportedAction", action, err)
		}
	}
}

func TestTranslateSuspendActivateConfigured(t *testing.T) {
	wg := WalledGardenConfig{Parameter: "Device.X_VENDOR_WalledGarden.Enable", SuspendValue: "true", ActiveValue: "false"}

	params, err := Translate("SUSPEND", map[string]string{}, wg, "")
	if err != nil {
		t.Fatalf("SUSPEND: %v", err)
	}
	if len(params) != 1 || params[0].Name != wg.Parameter || params[0].Value != "true" {
		t.Errorf("SUSPEND params = %+v, want [{%s true string}]", params, wg.Parameter)
	}

	params, err = Translate("ACTIVATE", map[string]string{}, wg, "")
	if err != nil {
		t.Fatalf("ACTIVATE: %v", err)
	}
	if len(params) != 1 || params[0].Name != wg.Parameter || params[0].Value != "false" {
		t.Errorf("ACTIVATE params = %+v, want [{%s false string}]", params, wg.Parameter)
	}
}

func TestTranslateSuspendRejectsNonWritableDiscoveredWalledGarden(t *testing.T) {
	wg := WalledGardenConfig{Parameter: "Device.X_VENDOR_WalledGarden.Enable", SuspendValue: "true", ActiveValue: "false"}
	_, err := TranslateWithCapabilities("SUSPEND", nil, wg, "", map[string]bool{wg.Parameter: false})
	if !errors.Is(err, ErrUnsupportedAction) {
		t.Errorf("err = %v, want ErrUnsupportedAction", err)
	}
}

func TestTranslateSuspendAcceptsWritableDiscoveredWalledGarden(t *testing.T) {
	wg := WalledGardenConfig{Parameter: "Device.X_VENDOR_WalledGarden.Enable", SuspendValue: "true", ActiveValue: "false"}
	params, err := TranslateWithCapabilities("SUSPEND", nil, wg, "", map[string]bool{wg.Parameter: true})
	if err != nil {
		t.Fatalf("TranslateWithCapabilities: %v", err)
	}
	if len(params) != 1 || params[0].Name != wg.Parameter {
		t.Errorf("params = %+v, want writable walled-garden parameter", params)
	}
}

func TestTranslateUnknownAction(t *testing.T) {
	_, err := Translate("DELETE_ACCOUNT", nil, WalledGardenConfig{}, "")
	if !errors.Is(err, ErrUnsupportedAction) {
		t.Errorf("err = %v, want ErrUnsupportedAction", err)
	}
}

// TestActionRegistryListsExactlyThreeActions pins the registry's
// contents so an accidental addition/removal is caught explicitly,
// rather than only being noticed via a downstream Translate test.
func TestActionRegistryListsExactlyThreeActions(t *testing.T) {
	want := map[string]bool{"MODIFY_WIFI": true, "SUSPEND": true, "ACTIVATE": true}
	if len(actionRegistry) != len(want) {
		t.Fatalf("actionRegistry has %d entries, want %d", len(actionRegistry), len(want))
	}
	for action := range want {
		if _, ok := actionRegistry[action]; !ok {
			t.Errorf("actionRegistry missing %q", action)
		}
	}
}
