package cpemetrics

import (
	"strings"
	"testing"
	"unicode/utf8"

	"acs/internal/cwmp"
	"acs/internal/devices/adapters"
)

const fm = "Device.Cellular.Interface.1."

func fastMileNames() map[string]bool {
	return map[string]bool{
		fm + "RSSI":                    false,
		fm + "CurrentAccessTechnology": false,
		fm + "X_ALU-COM_Attached5GCellStat.RSRPCurrent":  false,
		fm + "X_ALU-COM_Attached5GCellStat.RSRQCurrent":  false,
		fm + "X_ALU-COM_Attached5GCellStat.SNRCurrent":   false,
		fm + "X_ALU-COM_Attached5GCellStat.Band":         false,
		fm + "X_ALU-COM_Attached5GCellStat.AvgRSRPMonth": false,
		fm + "X_ALU-COM_AttachedLteCellStat.RSRPCurrent": false,
		fm + "X_ALU-COM_AttachedLteCellStat.RSRQCurrent": false,
		fm + "X_ALU-COM_AttachedLteCellStat.SNRCurrent":  false,
		fm + "X_ALU-COM_AttachedLteCellStat.Band":        false,
	}
}

const (
	g5  = fm + "X_ALU-COM_Attached5GCellStat."
	lte = fm + "X_ALU-COM_AttachedLteCellStat."
)

func TestPlanReadsPlansEveryObjectWith5GFirstAndSkipsUnresolved(t *testing.T) {
	plan := PlanReads("DEVICE2", fastMileNames())
	want := []Read{
		{adapters.CellularRSRP, g5 + "RSRPCurrent", 0},
		{adapters.CellularRSRP, lte + "RSRPCurrent", 1},
		{adapters.CellularRSRQ, g5 + "RSRQCurrent", 0},
		{adapters.CellularRSRQ, lte + "RSRQCurrent", 1},
		{adapters.CellularSNR, g5 + "SNRCurrent", 0},
		{adapters.CellularSNR, lte + "SNRCurrent", 1},
		{adapters.CellularRSSI, fm + "RSSI", 0},
		{adapters.CellularBand, g5 + "Band", 0},
		{adapters.CellularBand, lte + "Band", 1},
		{adapters.CellularRAT, fm + "CurrentAccessTechnology", 0},
	}
	if len(plan) != len(want) {
		t.Fatalf("plan = %v, want %v", plan, want)
	}
	for i := range want {
		if plan[i] != want[i] {
			t.Errorf("plan[%d] = %v, want %v", i, plan[i], want[i])
		}
	}
	if got := PlanReads("DEVICE2", map[string]bool{"Device.WiFi.Radio.1.Channel": true}); got != nil {
		t.Errorf("no cellular names: plan = %v, want nil", got)
	}
	if got := PlanReads("DEVICE2", nil); got != nil {
		t.Errorf("nil names: plan = %v, want nil", got)
	}
	if got := PlanReads("DEVICE2", map[string]bool{}); got != nil {
		t.Errorf("empty names: plan = %v, want nil", got)
	}
	if got := PlanReads("InternetGatewayDevice.", fastMileNames()); got != nil {
		t.Errorf("IGD root: plan = %v, want nil", got)
	}
}

func TestPlanReadsLTEOnlyDevicePlansOnlyLTEPaths(t *testing.T) {
	names := map[string]bool{}
	for k := range fastMileNames() {
		if !strings.Contains(k, "Attached5GCellStat") {
			names[k] = false
		}
	}
	plan := PlanReads("DEVICE2", names)
	if len(plan) == 0 {
		t.Fatal("empty plan")
	}
	for _, r := range plan {
		if strings.Contains(r.Path, "5G") {
			t.Errorf("planned a 5G path %s", r.Path)
		}
		if r.Rank != 0 {
			t.Errorf("%s rank = %d, want 0 (only one path per field)", r.Path, r.Rank)
		}
	}
}

func pv(name, value string) cwmp.ParameterValueStruct {
	return cwmp.ParameterValueStruct{Name: name, Value: value}
}

func TestParseStoresSentinelAsNil(t *testing.T) {
	reads := PlanReads("DEVICE2", fastMileNames())
	s, ok := Parse(reads, []cwmp.ParameterValueStruct{
		pv(g5+"RSRPCurrent", "-32768"),
		pv(g5+"SNRCurrent", "10"),
	})
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if s.RSRP != nil {
		t.Errorf("RSRP = %v, want nil", *s.RSRP)
	}
	if s.SNR == nil || *s.SNR != 10 {
		t.Errorf("SNR = %v, want 10", s.SNR)
	}
}

func TestParseReturnsNotOKWhenNothingUsable(t *testing.T) {
	reads := PlanReads("DEVICE2", fastMileNames())
	if _, ok := Parse(reads, nil); ok {
		t.Error("empty reply: ok = true")
	}
	_, ok := Parse(reads, []cwmp.ParameterValueStruct{
		pv(g5+"RSRPCurrent", "-32768"), pv(lte+"RSRPCurrent", "-32768"), pv(g5+"RSRQCurrent", "-32768"), pv(fm+"RSSI", "2147483647"),
	})
	if ok {
		t.Error("all sentinels: ok = true")
	}
}

func TestParseKeepsTextFields(t *testing.T) {
	reads := PlanReads("DEVICE2", fastMileNames())
	s, ok := Parse(reads, []cwmp.ParameterValueStruct{
		pv(fm+"X_ALU-COM_Attached5GCellStat.Band", " n78 "),
		pv(fm+"CurrentAccessTechnology", "NR5G-SA\n"),
	})
	if !ok || s.Band != "n78" || s.RAT != "NR5G-SA" {
		t.Errorf("got %+v ok=%v, want Band n78 RAT NR5G-SA", s, ok)
	}
}

func TestParseRejectsNonFiniteNumbers(t *testing.T) {
	reads := PlanReads("DEVICE2", fastMileNames())
	for _, bad := range []string{"NaN", "Inf", "-Inf", "+Inf", "infinity"} {
		for _, r := range reads {
			switch r.Field {
			case adapters.CellularBand, adapters.CellularRAT:
				continue
			}
			s, ok := Parse([]Read{r}, []cwmp.ParameterValueStruct{pv(r.Path, bad)})
			if ok {
				t.Errorf("%s=%q: ok = true, want false", r.Field, bad)
			}
			if s.RSRP != nil || s.RSRQ != nil || s.SNR != nil || s.RSSI != nil {
				t.Errorf("%s=%q: stored a non-finite metric: %+v", r.Field, bad, s)
			}
		}
	}
}

func TestParseKeepsNumericTextFieldsAndDropsSentinels(t *testing.T) {
	reads := PlanReads("DEVICE2", fastMileNames())
	s, ok := Parse(reads, []cwmp.ParameterValueStruct{
		pv(fm+"X_ALU-COM_Attached5GCellStat.Band", "78"),
		pv(fm+"CurrentAccessTechnology", " 5 "),
	})
	if !ok || s.Band != "78" || s.RAT != "5" {
		t.Errorf("got %+v ok=%v, want Band 78 RAT 5", s, ok)
	}
	s, ok = Parse(reads, []cwmp.ParameterValueStruct{
		pv(fm+"X_ALU-COM_Attached5GCellStat.Band", "4294967295"),
	})
	if ok || s.Band != "" {
		t.Errorf("sentinel band: got %+v ok=%v, want empty and not ok", s, ok)
	}
}

func TestTextIsBoundedAndCleaned(t *testing.T) {
	if got := text(strings.Repeat("A", 1<<20)); utf8.RuneCountInString(got) > 32 || got == "" {
		t.Errorf("1 MB band: %d runes, want 1..32", utf8.RuneCountInString(got))
	}
	if got := text(" n7\n8\t\x00\x1b[0m\r "); got != "n78[0m" {
		t.Errorf("control characters: got %q, want %q", got, "n78[0m")
	}
	got := text(strings.Repeat("é", 100) + "x")
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 32 {
		t.Errorf("multi-byte cut: valid=%v runes=%d, want valid and 32", utf8.ValidString(got), utf8.RuneCountInString(got))
	}
	if got := text("-32768"); got != "" {
		t.Errorf("sentinel: got %q, want empty", got)
	}
}

func TestParseFallsBackToLTEWhen5GIsASentinel(t *testing.T) {
	reads := PlanReads("DEVICE2", fastMileNames())
	s, ok := Parse(reads, []cwmp.ParameterValueStruct{
		pv(g5+"RSRPCurrent", "-32768"), pv(lte+"RSRPCurrent", "-101"),
		pv(g5+"RSRQCurrent", "-32768"), pv(lte+"RSRQCurrent", "-12"),
		pv(g5+"SNRCurrent", "-32768"), pv(lte+"SNRCurrent", "4"),
		pv(g5+"Band", "4294967295"), pv(lte+"Band", "B3"),
	})
	if !ok || s.RSRP == nil || *s.RSRP != -101 || s.RSRQ == nil || *s.RSRQ != -12 || s.SNR == nil || *s.SNR != 4 || s.Band != "B3" {
		t.Errorf("got %+v ok=%v, want the LTE values", s, ok)
	}
}

func TestParseTakes5GValueWhenBothAreValid(t *testing.T) {
	reads := PlanReads("DEVICE2", fastMileNames())
	// The reply order must not matter, only the plan's rank.
	s, _ := Parse(reads, []cwmp.ParameterValueStruct{
		pv(lte+"RSRPCurrent", "-101"), pv(g5+"RSRPCurrent", "-90"),
		pv(lte+"Band", "B3"), pv(g5+"Band", "n78"),
	})
	if s.RSRP == nil || *s.RSRP != -90 || s.Band != "n78" {
		t.Errorf("got %+v, want RSRP -90 and Band n78", s)
	}
}

func TestParseTakesBandFromTheObjectThatSuppliedRSRP(t *testing.T) {
	reads := PlanReads("DEVICE2", fastMileNames())
	s, _ := Parse(reads, []cwmp.ParameterValueStruct{
		pv(g5+"RSRPCurrent", "-32768"), pv(g5+"Band", "n78"),
		pv(lte+"RSRPCurrent", "-101"), pv(lte+"Band", "B3"),
	})
	if s.RSRP == nil || *s.RSRP != -101 || s.Band != "B3" {
		t.Errorf("got %+v, want RSRP -101 with the LTE band B3", s)
	}
	// RSRP from 5G but its object has no band: fall back to the first band.
	s, _ = Parse(reads, []cwmp.ParameterValueStruct{
		pv(g5+"RSRPCurrent", "-90"), pv(g5+"Band", "4294967295"), pv(lte+"Band", "B3"),
	})
	if s.Band != "B3" {
		t.Errorf("band fallback = %q, want B3", s.Band)
	}
	// No RSRP at all: first non-empty band by rank.
	s, ok := Parse(reads, []cwmp.ParameterValueStruct{pv(g5+"Band", "n78"), pv(lte+"Band", "B3")})
	if !ok || s.Band != "n78" {
		t.Errorf("no RSRP: got %+v ok=%v, want n78", s, ok)
	}
}

const fm2 = "Device.Cellular.Interface.2."

func twoInterfaceNames() map[string]bool {
	names := fastMileNames()
	names[fm2+"RSRP"] = false
	names[fm2+"RSSI"] = false
	names[fm2+"X_ALU-COM_Attached5GCellStat.RSRPCurrent"] = false
	return names
}

func TestPlanReadsPlansOnlyInterface1(t *testing.T) {
	for _, r := range PlanReads("DEVICE2", twoInterfaceNames()) {
		if strings.Contains(r.Path, "Interface.2.") {
			t.Errorf("planned %s", r.Path)
		}
	}
}

func TestParseDoesNotTakeAnotherInterfacesValue(t *testing.T) {
	reads := PlanReads("DEVICE2", twoInterfaceNames())
	s, ok := Parse(reads, []cwmp.ParameterValueStruct{
		pv(g5+"RSRPCurrent", "-32768"), pv(lte+"RSRPCurrent", "-32768"),
		pv(fm2+"RSRP", "-80"), pv(fm2+"X_ALU-COM_Attached5GCellStat.RSRPCurrent", "-81"),
	})
	if ok || s.RSRP != nil {
		t.Errorf("got %+v ok=%v, want nil RSRP and not ok", s, ok)
	}
}

func TestTextStripsControlCharactersBeforeTrimAndSentinel(t *testing.T) {
	if got := text("\x00 n78"); got != "n78" {
		t.Errorf("leading control then space: %q", got)
	}
	if got := text("\x00 \x01 "); got != "" {
		t.Errorf("only spaces and controls: %q, want empty", got)
	}
	if got := text("-3276\x008"); got != "" {
		t.Errorf("sentinel split by a control character: %q, want empty", got)
	}
}
