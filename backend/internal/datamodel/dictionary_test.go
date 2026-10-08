package datamodel

import "testing"

func TestOfficialDictionaries(t *testing.T) {
	for _, protocol := range []string{"cwmp", "usp"} {
		for _, version := range []string{"2.20", "2.21"} {
			d, err := Load(protocol, version)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"Device.DeviceInfo.SoftwareVersion", "Device.WiFi.SSID.7.Enable", "Device.Cellular.Interface.2.RSRP", "Device.BulkData.Profile.1.ReportingInterval", "Device.WiFi.DataElements.Network.Device.2."} {
				if _, ok := d.Lookup(path); !ok {
					t.Errorf("%s %s missing %s", protocol, version, path)
				}
			}
			if protocol == "usp" {
				e, ok := d.Lookup("Device.IP.Diagnostics.DownloadDiagnostics()")
				if !ok || e.Kind != "command" {
					t.Fatal("missing USP throughput command")
				}
			}
			if err := d.ValidateValue("Device.WiFi.SSID.7.Enable", "nope"); err == nil {
				t.Error("invalid boolean accepted")
			}
			if err := d.ValidateValue("Device.BulkData.Profile.1.ReportingInterval", "-1"); err == nil {
				t.Error("negative unsigned value accepted")
			}
			if err := d.ValidateValue("Device.WiFi.SSID.7.Enable", "true"); err != nil {
				t.Fatal(err)
			}
			if err := d.ValidateValue("Device.X_ACME.Radio.Mode", "new-firmware-value"); err != nil {
				t.Error("vendor extension was blocked")
			}
		}
	}
}
func TestNormalizePathDoesNotRewriteVendorNames(t *testing.T) {
	if got := NormalizePath("Device.X_123.Radio.12.Mode"); got != "Device.X_123.Radio.{i}.Mode" {
		t.Fatal(got)
	}
	if _, err := Load("cwmp", "99.1"); err == nil {
		t.Error("unbundled version accepted")
	}
}

func TestStandardRangesEnumsAndSizes(t *testing.T) {
	d, err := Load("cwmp", "2.21")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ path, value string }{{"Device.SessionManagement.PDU.1.SSC", "4"}, {"Device.Cellular.Interface.1.Mode", "invented-mode"}, {"Device.WiFi.SSID.1.SSID", "this-name-is-longer-than-thirty-two-bytes"}} {
		if d.ValidateValue(item.path, item.value) == nil {
			t.Fatal("invalid value accepted", item)
		}
	}
	if err := d.ValidateValue("Device.SessionManagement.PDU.1.SSC", "3"); err != nil {
		t.Fatal(err)
	}
}
