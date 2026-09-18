package main

import (
	"testing"

	"acs/internal/devices"
)

func TestFirmwareProfileAllowsDownload(t *testing.T) {
	tests := []struct {
		name      string
		device    *devices.Device
		wantAllow bool
	}{
		{name: "legacy or undiscovered device", device: &devices.Device{}, wantAllow: true},
		{name: "qualified model profile", device: &devices.Device{ProfileID: firmwareStrPtr("zyxel.nr7303-eu01v1f"), ProfileQualified: true}, wantAllow: true},
		{name: "vendor fallback profile", device: &devices.Device{ProfileID: firmwareStrPtr("zyxel"), ProfileQualified: false}, wantAllow: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firmwareProfileAllowsDownload(tt.device); got != tt.wantAllow {
				t.Fatalf("firmwareProfileAllowsDownload() = %t, want %t", got, tt.wantAllow)
			}
		})
	}
}

func firmwareStrPtr(s string) *string { return &s }
