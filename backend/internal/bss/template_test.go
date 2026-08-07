package bss

import (
	"errors"
	"testing"
)

func TestTranslateModifyWifi(t *testing.T) {
	params, err := Translate("MODIFY_WIFI", map[string]string{
		"wifi_ssid":     "Smith_Family_5G",
		"wifi_password": "SuperSecretPassword123",
	}, WalledGardenConfig{})
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

func TestTranslateModifyWifiPartial(t *testing.T) {
	params, err := Translate("MODIFY_WIFI", map[string]string{"wifi_ssid": "OnlySSID"}, WalledGardenConfig{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(params) != 1 || params[0].Name != "Device.WiFi.SSID.1.SSID" {
		t.Errorf("params = %+v, want just the SSID write", params)
	}
}

func TestTranslateModifyWifiRejectsEmptyParams(t *testing.T) {
	_, err := Translate("MODIFY_WIFI", map[string]string{}, WalledGardenConfig{})
	if !errors.Is(err, ErrInvalidParameters) {
		t.Errorf("err = %v, want ErrInvalidParameters", err)
	}
}

func TestTranslateSuspendActivateUnconfigured(t *testing.T) {
	for _, action := range []string{"SUSPEND", "ACTIVATE"} {
		_, err := Translate(action, map[string]string{}, WalledGardenConfig{})
		if !errors.Is(err, ErrUnsupportedAction) {
			t.Errorf("action %q: err = %v, want ErrUnsupportedAction", action, err)
		}
	}
}

func TestTranslateSuspendActivateConfigured(t *testing.T) {
	wg := WalledGardenConfig{Parameter: "Device.X_VENDOR_WalledGarden.Enable", SuspendValue: "true", ActiveValue: "false"}

	params, err := Translate("SUSPEND", map[string]string{}, wg)
	if err != nil {
		t.Fatalf("SUSPEND: %v", err)
	}
	if len(params) != 1 || params[0].Name != wg.Parameter || params[0].Value != "true" {
		t.Errorf("SUSPEND params = %+v, want [{%s true string}]", params, wg.Parameter)
	}

	params, err = Translate("ACTIVATE", map[string]string{}, wg)
	if err != nil {
		t.Fatalf("ACTIVATE: %v", err)
	}
	if len(params) != 1 || params[0].Name != wg.Parameter || params[0].Value != "false" {
		t.Errorf("ACTIVATE params = %+v, want [{%s false string}]", params, wg.Parameter)
	}
}

func TestTranslateUnknownAction(t *testing.T) {
	_, err := Translate("DELETE_ACCOUNT", nil, WalledGardenConfig{})
	if !errors.Is(err, ErrUnsupportedAction) {
		t.Errorf("err = %v, want ErrUnsupportedAction", err)
	}
}
