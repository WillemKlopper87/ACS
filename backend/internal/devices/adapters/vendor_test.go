package adapters

import (
	"strings"
	"testing"
)

// TestGenericCellularFallbackUsesRealTR181Paths pins the fallback to paths
// that actually exist in TR-181 Device:2. RSSI/RSRP/RSRQ live directly on
// Device.Cellular.Interface.{i}; the Stats. sub-object holds only
// byte/packet counters, and SINR is not in the data model at all.
func TestGenericCellularFallbackUsesRealTR181Paths(t *testing.T) {
	want := []string{
		"Device.Cellular.Interface.1.RSRP",
		"Device.Cellular.Interface.1.RSRQ",
		"Device.Cellular.Interface.1.RSSI",
	}
	if len(genericCellularFallback) != len(want) {
		t.Fatalf("genericCellularFallback has %d entries, want %d: %v",
			len(genericCellularFallback), len(want), genericCellularFallback)
	}
	for i, w := range want {
		if genericCellularFallback[i] != w {
			t.Errorf("genericCellularFallback[%d] = %q, want %q", i, genericCellularFallback[i], w)
		}
	}
}

// TestGenericCellularFallbackAvoidsStatsAndSNR guards the two specific
// mistakes that were shipped: signal metrics under .Stats., and a
// non-existent SNR-family field -- TR-181 defines neither. The field that
// actually shipped was spelled "SNR", not "SINR", and "SNR" is not a
// substring of "SINR" (or vice versa: "SINR" is not a substring of "SNR"),
// so the guard checks both spellings explicitly rather than relying on one
// substring check to catch the other. RSRQ/RSRP/RSSI must not trip it --
// none of those contain "N", so neither spelling matches them.
func TestGenericCellularFallbackAvoidsStatsAndSNR(t *testing.T) {
	for _, p := range genericCellularFallback {
		if strings.Contains(p, ".Stats.") {
			t.Errorf("%q puts a signal metric under .Stats., which holds only packet counters", p)
		}
		upper := strings.ToUpper(p)
		if strings.Contains(upper, "SNR") || strings.Contains(upper, "SINR") {
			t.Errorf("%q uses SNR/SINR, which is not defined anywhere in TR-181", p)
		}
	}
}
