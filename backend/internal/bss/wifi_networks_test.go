package bss

import (
	"errors"
	"testing"
)

func dualBandView() *WiFiNetworksView {
	net := func(i int, band string) WiFiNetworkTarget {
		n := map[int]string{1: "1", 2: "2"}[i]
		return WiFiNetworkTarget{Instance: i, Band: band,
			NamePath: "Device.WiFi.SSID." + n + ".SSID", NameWritable: true,
			EnablePath: "Device.WiFi.SSID." + n + ".Enable", EnableWritable: true,
			PassphrasePath: "Device.WiFi.AccessPoint." + n + ".Security.KeyPassphrase", PassphraseWritable: true}
	}
	return &WiFiNetworksView{Supported: true, Complete: true, Primary: []WiFiNetworkTarget{net(1, "2.4GHz"), net(2, "5GHz")}}
}

func TestModifyWifiWritesEveryPrimaryBand(t *testing.T) {
	writes, err := TranslateModifyWifiNetworks(map[string]string{"wifi_ssid": "Home", "wifi_password": "s3cret-pass", "wifi_enabled": "true"}, dualBandView())
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 6 {
		t.Fatalf("writes = %+v, want name+passphrase+enable on both bands", writes)
	}
	for _, w := range writes {
		if w.Name == "Device.WiFi.SSID.2.Enable" && (w.Value != "1" || w.Type != "xsd:boolean") {
			t.Fatalf("enable write = %+v, want xsd:boolean 1", w)
		}
	}
}

func TestModifyWifiRefusesIncompleteOrUnsupported(t *testing.T) {
	incomplete := dualBandView()
	incomplete.Complete = false
	for name, view := range map[string]*WiFiNetworksView{
		"incomplete":  incomplete,
		"unsupported": {Supported: false, Reason: "run parameter discovery first"},
		"no primary":  {Supported: true, Complete: true},
	} {
		if _, err := TranslateModifyWifiNetworks(map[string]string{"wifi_ssid": "x"}, view); !errors.Is(err, ErrUnsupportedAction) {
			t.Errorf("%s: err = %v, want ErrUnsupportedAction", name, err)
		}
	}
}
