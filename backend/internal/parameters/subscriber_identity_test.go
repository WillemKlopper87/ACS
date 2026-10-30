package parameters

import "testing"

func TestSubscriberIdentifierKind(t *testing.T) {
	cases := map[string]string{
		"Device.Cellular.Interface.1.USIM.IMSI":                              SubscriberIMSI,
		"Device.Cellular.Interface.1.X_ALU-COM_CurrentUsedIMSI":              SubscriberIMSI,
		"Device.Cellular.Interface.1.USIM.ICCID":                             SubscriberICCID,
		"Device.Cellular.Interface.1.X_ALU-COM_ESIM.ActiveProfileICCID":      SubscriberICCID,
		"Device.Cellular.Interface.1.X_ALU-COM_ESIM.CurrentUsedProfileICCID": SubscriberICCID,
		"Device.Cellular.Interface.1.USIM.MSISDN":                            SubscriberMSISDN,
		"Device.Cellular.Interface.1.IMEI":                                   SubscriberIMEI,
		"Device.Cellular.Interface.1.IMEISV":                                 SubscriberIMEI,
		"Device.Cellular.Interface.1.X_ALU-COM_ESIM.Eid":                     SubscriberEID,
		"Device.Cellular.Interface.1.USIM.PIN":                               SubscriberSecret,
		"Device.Cellular.Interface.1.USIM.PINCheck":                          SubscriberSecret,
		"Device.Cellular.Interface.1.USIM.Status":                            "",
		"Device.Cellular.Interface.1.X_ALU-COM_MCC":                          "",
		"Device.DeviceInfo.SerialNumber":                                     "",
		"Device.WiFi.SSID.1.SSID":                                            "",
	}
	for path, want := range cases {
		if got := SubscriberIdentifierKind(path); got != want {
			t.Errorf("SubscriberIdentifierKind(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestMaskSubscriberIdentifier(t *testing.T) {
	cases := []struct{ kind, in, want string }{
		{SubscriberIMSI, "655010123456789", "65501••••••6789"},
		{SubscriberIMEI, "356938035643809", "35693803•••3809"},
		{SubscriberICCID, "8927010000123456789", "89•••••••••••••6789"},
		{SubscriberMSISDN, "27821234567", "•••••••4567"},
		{SubscriberSecret, "1234", ""},
		{SubscriberIMSI, "123", "•••"},
		{"", " untouched ", " untouched "},
	}
	for _, c := range cases {
		if got := MaskSubscriberIdentifier(c.kind, c.in); got != c.want {
			t.Errorf("MaskSubscriberIdentifier(%q, %q) = %q, want %q", c.kind, c.in, got, c.want)
		}
	}
}

func TestRedactSubscriberIdentifiers(t *testing.T) {
	in := map[string]CachedValue{
		"Device.Cellular.Interface.1.USIM.IMSI": {Value: "655010123456789"},
		"Device.Cellular.Interface.1.USIM.PIN":  {Value: "1234"},
		"Device.DeviceInfo.SoftwareVersion":     {Value: "1.2501.00.0798"},
	}
	masked := RedactSubscriberIdentifiers(in, false)
	if masked["Device.Cellular.Interface.1.USIM.IMSI"].Value != "65501••••••6789" {
		t.Fatalf("IMSI not masked: %q", masked["Device.Cellular.Interface.1.USIM.IMSI"].Value)
	}
	if in["Device.Cellular.Interface.1.USIM.IMSI"].Value != "655010123456789" {
		t.Fatal("input map modified")
	}
	full := RedactSubscriberIdentifiers(in, true)
	if full["Device.Cellular.Interface.1.USIM.IMSI"].Value != "655010123456789" {
		t.Fatal("full view masked the IMSI")
	}
	for _, view := range []map[string]CachedValue{masked, full} {
		if view["Device.Cellular.Interface.1.USIM.PIN"].Value != "" {
			t.Fatal("SIM PIN must never be returned")
		}
		if view["Device.DeviceInfo.SoftwareVersion"].Value != "1.2501.00.0798" {
			t.Fatal("non-identifier value changed")
		}
	}
}
