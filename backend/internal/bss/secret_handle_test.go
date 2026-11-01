package bss

import (
	"errors"
	"testing"

	"acs/internal/devices"
)

func TestTranslateModifyWifiSecretHandle(t *testing.T) {
	params, err := Translate("MODIFY_WIFI", map[string]string{"wifi_password_handle": "sh_abc"}, WalledGardenConfig{}, devices.DataModelRootDevice2)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(params) != 1 || params[0].SecretHandle != "sh_abc" || params[0].Value != "" {
		t.Fatalf("params = %+v, want one write with the handle and no value", params)
	}
	if _, err := Translate("MODIFY_WIFI", map[string]string{"wifi_password": "x", "wifi_password_handle": "sh_abc"}, WalledGardenConfig{}, devices.DataModelRootDevice2); !errors.Is(err, ErrInvalidParameters) {
		t.Fatalf("value and handle: %v, want ErrInvalidParameters", err)
	}
}

func TestTranslateModifyWifiNetworksSecretHandle(t *testing.T) {
	view := &WiFiNetworksView{Supported: true, Complete: true, Primary: []WiFiNetworkTarget{
		{Instance: 1, PassphrasePath: "Device.WiFi.AccessPoint.1.Security.KeyPassphrase", PassphraseWritable: true},
		{Instance: 2, PassphrasePath: "Device.WiFi.AccessPoint.2.Security.KeyPassphrase", PassphraseWritable: true},
	}}
	params, err := TranslateModifyWifiNetworks(map[string]string{"wifi_password_handle": "sh_abc"}, view)
	if err != nil {
		t.Fatalf("TranslateModifyWifiNetworks: %v", err)
	}
	if len(params) != 2 || params[0].SecretHandle != "sh_abc" || params[1].SecretHandle != "sh_abc" {
		t.Fatalf("params = %+v, want the handle on both bands", params)
	}
	if got := SecretHandles(params); len(got) != 1 || got[0] != "sh_abc" {
		t.Fatalf("SecretHandles = %v, want one", got)
	}
}
