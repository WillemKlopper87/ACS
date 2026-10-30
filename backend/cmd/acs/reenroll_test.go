package main

import (
	"testing"
	"time"

	"acs/internal/devices"
)

// A factory-reset CPE returns with the bootstrap identity. Without an open
// re-enrolment window an already-informed device must keep rejecting it.
func TestBootstrapDeviceIsEstablishedHonoursReenrollmentWindow(t *testing.T) {
	informed := time.Now().Add(-time.Hour)
	future := time.Now().Add(30 * time.Minute)
	past := time.Now().Add(-time.Minute)

	if !bootstrapDeviceIsEstablished(&devices.Device{LastInformAt: &informed}) {
		t.Fatal("an informed device must be established")
	}
	if bootstrapDeviceIsEstablished(&devices.Device{LastInformAt: &informed, ReenrollUntil: &future}) {
		t.Fatal("an open re-enrolment window must allow bootstrap graduation")
	}
	if !bootstrapDeviceIsEstablished(&devices.Device{LastInformAt: &informed, ReenrollUntil: &past}) {
		t.Fatal("an expired re-enrolment window must not allow bootstrap")
	}
	if bootstrapDeviceIsEstablished(&devices.Device{CWMPAuthMode: devices.AuthModeNone}) {
		t.Fatal("a never-informed bootstrap registration is not established")
	}
}
