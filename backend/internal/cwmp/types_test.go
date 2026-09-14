package cwmp

import "testing"

func TestNormalizeOUI(t *testing.T) {
	cases := map[string]string{
		"001349":     "001349",
		"00:13:49":   "001349",
		"00-13-49":   "001349",
		"AbCdEf":     "ABCDEF",
		"aa:bb:cc":   "AABBCC",
		"aa-bb-cc":   "AABBCC",
		"  001349  ": "001349",
	}
	for in, want := range cases {
		if got := NormalizeOUI(in); got != want {
			t.Errorf("NormalizeOUI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNaturalKey_OUICaseAndSeparatorInsensitive(t *testing.T) {
	// Real Huawei-observed variance: the same physical device's OUI can
	// arrive in different case or with colon separators across Informs.
	// All of these must produce the identical natural key, or the same
	// device looks like several different ones to the platform.
	forms := []DeviceID{
		{OUI: "001349", ProductClass: "NR5103", SerialNumber: "S230Q12345678"},
		{OUI: "001349", ProductClass: "NR5103", SerialNumber: "S230Q12345678"},
		{OUI: "00:13:49", ProductClass: "NR5103", SerialNumber: "S230Q12345678"},
		{OUI: "00-13-49", ProductClass: "NR5103", SerialNumber: "S230Q12345678"},
	}
	want := forms[0].NaturalKey()
	for i, d := range forms[1:] {
		if got := d.NaturalKey(); got != want {
			t.Errorf("forms[%d].NaturalKey() = %q, want %q (same physical device, different OUI form)", i+1, got, want)
		}
	}
}

func TestNaturalKey_SerialNumberCaseIsNotNormalized(t *testing.T) {
	// Only OUI is normalized -- SerialNumber/ProductClass are vendor-
	// assigned strings, not hex codes, and are genuinely case-sensitive
	// identity components. Normalizing them would risk merging two
	// distinct real devices onto one key.
	a := DeviceID{OUI: "001349", SerialNumber: "AbC123"}
	b := DeviceID{OUI: "001349", SerialNumber: "abc123"}
	if a.NaturalKey() == b.NaturalKey() {
		t.Errorf("differently-cased serial numbers collided onto one natural key: %q", a.NaturalKey())
	}
}

func TestDeviceID_Normalized(t *testing.T) {
	d := DeviceID{OUI: "aa:bb:cc", ProductClass: "X", SerialNumber: "y"}
	got := d.Normalized()
	if got.OUI != "AABBCC" {
		t.Errorf("Normalized().OUI = %q, want AABBCC", got.OUI)
	}
	if got.ProductClass != "X" || got.SerialNumber != "y" {
		t.Errorf("Normalized() changed a field it shouldn't have: %+v", got)
	}
	if d.OUI != "aa:bb:cc" {
		t.Errorf("Normalized() mutated the receiver: original OUI = %q", d.OUI)
	}
}
