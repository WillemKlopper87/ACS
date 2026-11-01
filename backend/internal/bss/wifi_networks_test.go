package bss

import (
	"errors"
	"strings"
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

func TestPrimarySSIDNames(t *testing.T) {
	name := func(s string) *string { return &s }
	view := dualBandView()
	view.Primary[0].Name, view.Primary[1].Name = name("SENT-5G"), name("SENT-5G")
	if got, reason := PrimarySSIDNames(view); len(got) != 1 || got[0] != "SENT-5G" || reason != "" {
		t.Fatalf("shared name across bands = %v %q, want [SENT-5G]", got, reason)
	}
	view.Primary[1].Name = name("SENT-5G-5")
	if got, _ := PrimarySSIDNames(view); len(got) != 2 || got[1] != "SENT-5G-5" {
		t.Errorf("split SSID = %v, want both names in instance order", got)
	}

	for _, tc := range []struct {
		name string
		view *WiFiNetworksView
		want string
	}{
		{"no wifi of its own", &WiFiNetworksView{Supported: false}, "managed by gateway"},
		{"undiscovered", &WiFiNetworksView{Supported: false, Reason: "run parameter discovery first"}, "run parameter discovery first"},
		{"roles unread", &WiFiNetworksView{Supported: true, Complete: false, Primary: dualBandView().Primary}, "not read yet"},
		{"no primary", &WiFiNetworksView{Supported: true, Complete: true}, "no primary"},
		{"names uncached", dualBandView(), "names are not read yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := PrimarySSIDNames(tc.view)
			if got != nil || !strings.Contains(reason, tc.want) {
				t.Fatalf("= %v %q, want no names and a reason containing %q", got, reason, tc.want)
			}
		})
	}
}
