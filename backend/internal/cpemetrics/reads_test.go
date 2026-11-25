package cpemetrics

import (
	"testing"

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

func TestPlanReadsPrefers5GAndSkipsUnresolved(t *testing.T) {
	plan := PlanReads("DEVICE2", fastMileNames())
	want := []Read{
		{adapters.CellularRSRP, fm + "X_ALU-COM_Attached5GCellStat.RSRPCurrent"},
		{adapters.CellularRSRQ, fm + "X_ALU-COM_Attached5GCellStat.RSRQCurrent"},
		{adapters.CellularSNR, fm + "X_ALU-COM_Attached5GCellStat.SNRCurrent"},
		{adapters.CellularRSSI, fm + "RSSI"},
		{adapters.CellularBand, fm + "X_ALU-COM_Attached5GCellStat.Band"},
		{adapters.CellularRAT, fm + "CurrentAccessTechnology"},
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

func pv(name, value string) cwmp.ParameterValueStruct {
	return cwmp.ParameterValueStruct{Name: name, Value: value}
}

func TestParseStoresSentinelAsNil(t *testing.T) {
	reads := PlanReads("DEVICE2", fastMileNames())
	s, ok := Parse(reads, []cwmp.ParameterValueStruct{
		pv(reads[0].Path, "-32768"),
		pv(reads[2].Path, "10"),
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
		pv(reads[0].Path, "-32768"), pv(reads[1].Path, "-32768"), pv(reads[3].Path, "2147483647"),
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
