package adapters

import (
	"acs/internal/datamodel"
	"acs/internal/parameters"
	"testing"
	"time"
)

func TestFiveGPreservesUnavailableSignalUnitsAndFreshness(t *testing.T) {
	dictionary, err := datamodel.Load("cwmp", "2.21")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	names := map[string]bool{"Device.Cellular.Interface.{i}.RSRP": false, "Device.Cellular.Interface.1.Mode": false, "Device.SessionManagement.PDU.2.NetworkSlice.SliceServiceType": false}
	cache := map[string]parameters.CachedValue{"Device.Cellular.Interface.1.RSRP": {Value: "2147483647", UpdatedAt: now, Source: "INFORM"}, "Device.Cellular.Interface.1.Mode": {Value: "SA", UpdatedAt: now.Add(-time.Hour)}, "Device.SessionManagement.PDU.2.NetworkSlice.SliceServiceType": {Value: "1", UpdatedAt: now}}
	metrics := ProjectFiveG(dictionary, names, cache, now, 5*time.Minute)
	found := map[string]FiveGMetric{}
	for _, m := range metrics {
		found[m.Path] = m
	}
	if m := found["Device.Cellular.Interface.1.RSRP"]; m.State != "unavailable" || m.Value != nil || m.Unit != "dBm" {
		t.Fatal(m)
	}
	if found["Device.Cellular.Interface.1.Mode"].State != "stale" {
		t.Fatal("stale value hidden")
	}
	if found["Device.SessionManagement.PDU.2.NetworkSlice.SliceServiceType"].Category != "slice" {
		t.Fatal("slice mapping missing")
	}
}
