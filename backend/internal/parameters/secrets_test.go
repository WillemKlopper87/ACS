package parameters

import "testing"

func TestIsSecretParameter(t *testing.T) {
	secret := []string{
		"Device.WiFi.AccessPoint.1.Security.KeyPassphrase",
		"Device.WiFi.AccessPoint.1.Security.PreSharedKey",
		"Device.ManagementServer.ConnectionRequestPassword",
		"Device.ManagementServer.Password",
		"Device.XMPP.Connection.1.Password",
		"Device.Cellular.Interface.1.USIM.X_VENDOR_SecretKey",
	}
	for _, name := range secret {
		if !IsSecretParameter(name) {
			t.Errorf("%s not secret", name)
		}
	}
	for _, name := range []string{"Device.WiFi.SSID.1.SSID", "Device.ManagementServer.Username", "Device.Cellular.Interface.1.USIM.IMSI"} {
		if IsSecretParameter(name) {
			t.Errorf("%s reported secret", name)
		}
	}
}

func TestRedactSecrets(t *testing.T) {
	in := map[string]CachedValue{
		"Device.WiFi.AccessPoint.1.Security.KeyPassphrase":  {Value: "hunter2hunter2", Source: SourceSetValues},
		"Device.ManagementServer.ConnectionRequestPassword": {Value: ""}, // CPEs return "" on GPV
		"Device.WiFi.SSID.1.SSID":                           {Value: "SENT-5G"},
	}
	out := redactSecrets(in)
	if out["Device.WiFi.AccessPoint.1.Security.KeyPassphrase"].Value != SecretRedacted {
		t.Fatal("passphrase stored in plain text")
	}
	if out["Device.ManagementServer.ConnectionRequestPassword"].Value != "" {
		t.Fatal("empty secret must stay empty")
	}
	if out["Device.WiFi.SSID.1.SSID"].Value != "SENT-5G" {
		t.Fatal("non-secret changed")
	}
	if in["Device.WiFi.AccessPoint.1.Security.KeyPassphrase"].Value != "hunter2hunter2" {
		t.Fatal("caller's map modified")
	}
	plain := map[string]CachedValue{"Device.WiFi.SSID.1.SSID": {Value: "x"}}
	if got := redactSecrets(plain); len(got) != 1 || got["Device.WiFi.SSID.1.SSID"].Value != "x" {
		t.Fatal("no-secret map altered")
	}
}
