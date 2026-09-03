package main

import (
	"bytes"
	"log/slog"
	"testing"
)

func TestOnboardingListenerModes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	off := newOnboardingListener("off", logger)
	if off.active.Load() {
		t.Fatal("off mode should not be active")
	}

	on := newOnboardingListener("on", logger)
	on.onboarded("device-1", "127.0.0.1:1")
	if !on.active.Load() {
		t.Fatal("on mode should remain active after onboarding")
	}

	once := newOnboardingListener("once", logger)
	once.onboarded("device-1", "127.0.0.1:1")
	if once.active.Load() {
		t.Fatal("once mode should disable after successful onboarding")
	}

	invalid := newOnboardingListener("unexpected", logger)
	if invalid.active.Load() || invalid.mode != "off" {
		t.Fatal("invalid mode should be disabled")
	}
}
