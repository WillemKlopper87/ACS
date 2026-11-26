package simwatch

import (
	"strings"
	"testing"
)

const (
	iccidA = "8927000000000000011"
	iccidB = "8927000000000000022"
	imsiA  = "655010000000011"
	imsiB  = "655010000000022"
	imsiX  = "999990000000033" // a network the plan does not allow
)

func allowSA(imsi string) bool { return strings.HasPrefix(imsi, "65501") }

func TestJudge(t *testing.T) {
	cases := []struct {
		name     string
		cached   Identity
		incoming Identity
		want     Kind
		network  string
	}{
		{"same SIM", Identity{iccidA, imsiA}, Identity{iccidA, imsiA}, KindNone, "65501"},
		{"first SIM allowed", Identity{}, Identity{iccidA, imsiA}, KindBaseline, "65501"},
		{"first SIM not allowed", Identity{}, Identity{iccidA, imsiX}, KindAlert, "99999"},
		{"ICCID changed allowed", Identity{iccidA, imsiA}, Identity{iccidB, imsiA}, KindNotice, "65501"},
		{"IMSI changed not allowed", Identity{iccidA, imsiA}, Identity{iccidA, imsiX}, KindAlert, "99999"},
		{"swap back to the previous SIM", Identity{iccidB, imsiB}, Identity{iccidA, imsiA}, KindNotice, "65501"},
		{"only the new IMSI arrives", Identity{iccidA, imsiA}, Identity{"", imsiB}, KindNotice, "65501"},
		{"incoming empty", Identity{iccidA, imsiA}, Identity{}, KindNone, "65501"},
		{"nothing at all", Identity{}, Identity{}, KindNone, ""},
		{"ICCID changed, no IMSI known", Identity{iccidA, ""}, Identity{iccidB, ""}, KindNotice, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Judge(c.cached, c.incoming, allowSA)
			if got.Kind != c.want || got.Network != c.network {
				t.Fatalf("Judge = %+v, want kind %q network %q", got, c.want, c.network)
			}
		})
	}
}

func TestJudgeNilAllowedAllowsEverything(t *testing.T) {
	got := Judge(Identity{iccidA, imsiA}, Identity{iccidA, imsiX}, nil)
	if got.Kind != KindNotice {
		t.Fatalf("with no allow rule a changed SIM is a notice, got %q", got.Kind)
	}
}

func TestExtractIdentityIgnoresOtherInterfacesWhateverTheMapOrder(t *testing.T) {
	values := map[string]string{
		"Device.Cellular.Interface.1.USIM.ICCID": iccidA,
		"Device.Cellular.Interface.1.USIM.IMSI":  imsiA,
		"Device.Cellular.Interface.2.USIM.ICCID": iccidB,
		"Device.Cellular.Interface.2.USIM.IMSI":  imsiB,
	}
	for i := range 50 {
		if got := ExtractIdentity(values); got != (Identity{iccidA, imsiA}) {
			t.Fatalf("run %d: got %+v", i, got)
		}
	}
}

func TestExtractIdentityVendorIMSIOnlyWhenStandardAbsent(t *testing.T) {
	vendor := "Device.Cellular.Interface.1.X_ALU-COM_CurrentUsedIMSI"
	got := ExtractIdentity(map[string]string{vendor: imsiB})
	if got.IMSI != imsiB {
		t.Fatalf("vendor IMSI not used: %+v", got)
	}
	got = ExtractIdentity(map[string]string{vendor: imsiB, "Device.Cellular.Interface.1.USIM.IMSI": imsiA})
	if got.IMSI != imsiA {
		t.Fatalf("standard IMSI must win: %+v", got)
	}
}

func TestExtractIdentitySentinelsAndWhitespaceAreAbsent(t *testing.T) {
	got := ExtractIdentity(map[string]string{
		"Device.Cellular.Interface.1.USIM.ICCID": "-32768",
		"Device.Cellular.Interface.1.USIM.IMSI":  "  " + imsiA + "\n",
	})
	if got != (Identity{"", imsiA}) {
		t.Fatalf("got %+v", got)
	}
}

func TestReadPaths(t *testing.T) {
	if got := ReadPaths(nil); got != nil {
		t.Fatalf("nil names must give nil, got %v", got)
	}
	if got := ReadPaths(map[string]bool{"Device.WiFi.Radio.1.Enable": true}); got != nil {
		t.Fatalf("no SIM paths must give nil, got %v", got)
	}
	names := map[string]bool{
		"Device.Cellular.Interface.1.USIM.IMSI":                 true,
		"Device.Cellular.Interface.1.USIM.ICCID":                true,
		"Device.Cellular.Interface.1.X_ALU-COM_CurrentUsedIMSI": true,
		"Device.Cellular.Interface.2.USIM.IMSI":                 true,
	}
	got := ReadPaths(names)
	want := []string{
		"Device.Cellular.Interface.1.USIM.ICCID",
		"Device.Cellular.Interface.1.USIM.IMSI",
		"Device.Cellular.Interface.1.X_ALU-COM_CurrentUsedIMSI",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestHintsAreMasked(t *testing.T) {
	iccid, imsi := Hints(Identity{iccidA, imsiA})
	if iccid == "" || imsi == "" {
		t.Fatalf("hints empty: %q %q", iccid, imsi)
	}
	if strings.Contains(iccid, iccidA) || strings.Contains(imsi, imsiA) {
		t.Fatalf("hint carries the full identifier: %q %q", iccid, imsi)
	}
}

func TestNetwork(t *testing.T) {
	if Network(imsiA) != "65501" || Network("1234") != "" {
		t.Fatalf("Network wrong: %q %q", Network(imsiA), Network("1234"))
	}
}
