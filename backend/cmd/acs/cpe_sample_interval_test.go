package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestCPESampleIntervalFromEnv(t *testing.T) {
	cases := []struct {
		name, value string
		want        time.Duration
		warns       bool
	}{
		{"unset", "", 15 * time.Minute, false},
		{"valid", "5m", 5 * time.Minute, false},
		{"zero disables", "0", 0, false},
		{"no unit", "15", 15 * time.Minute, true},
		{"negative", "-5m", 15 * time.Minute, true},
		{"garbage", "often", 15 * time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ACS_CPE_SAMPLE_INTERVAL", tc.value)
			var buf bytes.Buffer
			got := cpeSampleIntervalFromEnv(slog.New(slog.NewTextHandler(&buf, nil)))
			if got != tc.want {
				t.Fatalf("interval = %v, want %v", got, tc.want)
			}
			warned := strings.Contains(buf.String(), "ACS_CPE_SAMPLE_INTERVAL") && strings.Contains(buf.String(), "15m0s")
			if warned != tc.warns {
				t.Fatalf("warned = %v, want %v; log: %s", warned, tc.warns, buf.String())
			}
		})
	}
}
