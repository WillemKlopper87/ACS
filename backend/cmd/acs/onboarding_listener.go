package main

import (
	"log/slog"
	"net/http"
	"sync/atomic"
)

// onboardingListener is an opt-in reachability probe for field onboarding.
// It is deliberately attached to the existing CWMP endpoint so a CPE tests
// the same address, TLS, proxy, and authentication path used in production.
type onboardingListener struct {
	mode   string
	active atomic.Bool
	logger *slog.Logger
}

func newOnboardingListener(mode string, logger *slog.Logger) *onboardingListener {
	listener := &onboardingListener{mode: mode, logger: logger}
	listener.active.Store(mode == "on" || mode == "once")
	if mode != "off" && mode != "on" && mode != "once" {
		listener.mode = "off"
		listener.active.Store(false)
		logger.Warn("invalid ACS_ONBOARDING_LISTENER; disabled", "value", mode)
	}
	if listener.active.Load() {
		logger.Info("onboarding reachability listener enabled", "mode", listener.mode)
	}
	return listener
}

func (l *onboardingListener) observe(r *http.Request) {
	if l == nil || !l.active.Load() {
		return
	}
	l.logger.Info("CPE reached ACS onboarding listener", "remote", r.RemoteAddr, "path", r.URL.Path, "method", r.Method)
}

func (l *onboardingListener) onboarded(deviceID, remote string) {
	if l == nil || !l.active.Load() {
		return
	}
	l.logger.Info("CPE reached onboarding listener", "device_id", deviceID, "remote", remote)
	if l.mode == "once" {
		l.active.Store(false)
		l.logger.Info("onboarding reachability listener disabled after successful Inform")
	}
}
