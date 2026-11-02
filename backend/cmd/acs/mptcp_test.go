package main

import (
	"os"
	"strings"
	"testing"
)

// TestMultipathTCPOff pins go.mod's `godebug multipathtcp=0`: every ACS
// listener must stay plain TCP until all hosts run a kernel fixed for
// CVE-2026-98432 (exploited through MPTCP listeners).
func TestMultipathTCPOff(t *testing.T) {
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(mod), "\n") {
		if strings.TrimSpace(line) == "godebug multipathtcp=0" {
			return
		}
	}
	t.Fatal("go.mod no longer sets `godebug multipathtcp=0`; Go would listen with multipath TCP by default")
}
