package bss

import (
	"errors"
	"testing"
	"time"

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

func TestReconciliationNeverReturnsASecret(t *testing.T) {
	const path = "Device.WiFi.AccessPoint.1.Security.KeyPassphrase"
	orders := []OrderRecord{{ExternalOrderID: "o1", CommandKey: "ck", Parameters: []ParameterWrite{{Name: path, Value: "plain-passphrase"}}}}
	for _, confirmed := range []bool{true, false} {
		got := ComputeReconciliation("dev", orders, map[string]bool{"ck": confirmed}, map[string]CachedParameter{path: {Value: "plain-passphrase"}}, time.Now())
		if f := got.Fields[0]; f.IntendedValue != SecretValueRedacted || f.ActualValue != "" {
			t.Fatalf("confirmed=%v: field %+v exposes the passphrase", confirmed, f)
		}
	}
}

func TestRedactSecretWrites(t *testing.T) {
	in := []ParameterWrite{{Name: "Device.WiFi.SSID.1.SSID", Value: "Home"}, {Name: "X.KeyPassphrase", Value: "secret-value"}, {Name: "Y.KeyPassphrase", SecretHandle: "sh_x"}}
	out := RedactSecretWrites(in)
	if out[0].Value != "Home" || out[1].Value != SecretValueRedacted || out[2].Value != "" || out[2].SecretHandle != "sh_x" || in[1].Value != "secret-value" {
		t.Fatalf("redacted = %+v (input %+v)", out, in)
	}
}

func TestUTCTimestamp(t *testing.T) {
	if got := utcTimestamp("2026-11-01T20:26:12+02:00"); got != "2026-11-01T18:26:12Z" {
		t.Fatalf("got %q", got)
	}
	if got := utcTimestamp("not a time"); got != "not a time" {
		t.Fatalf("got %q", got)
	}
}
