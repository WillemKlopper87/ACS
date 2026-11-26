package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/simwatch"
	"acs/internal/simwatch/recorder"
)

// simWatchSampler makes sure a cellular CPE's SIM identity is read at check-ins
// so a replaced SIM is noticed. The judging itself happens at the cache write
// (judgeSIM), so any reply carrying the identity counts, whoever asked for it.
type simWatchSampler struct {
	rec      *recorder.Repository
	interval time.Duration
	// lastTried throttles the per-Inform DB checks; it is process-local, so a
	// restart costs one extra check. The stored last_checked_at decides due-ness.
	lastTried sync.Map
}

// simWatchIntervalFromEnv reads ACS_SIM_WATCH_INTERVAL (default 1 hour, 0
// disables). An unparsable or negative value is logged and the default used.
func simWatchIntervalFromEnv(logger *slog.Logger) time.Duration {
	const key = "ACS_SIM_WATCH_INTERVAL"
	def := time.Hour
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" && raw != "0" {
		if d, err := time.ParseDuration(raw); err != nil || d < 0 {
			logger.Warn("invalid duration, using the default", "variable", key, "value", raw, "default", def.String())
		}
	}
	return envOrDuration(key, def)
}

// maybeQueueSIMWatch queues one read of the interface 1 SIM identity when the
// last check is older than the interval (less a small slack, as for the
// signal sampler), nothing is pending, no discovery walk is running and the
// discovered model has identity paths. It never falls back to default paths:
// a path the device lacks would fail the whole read.
func (h *handler) maybeQueueSIMWatch(ctx context.Context, device *devices.Device) {
	s := h.simWatch
	if s == nil || s.rec == nil || s.interval <= 0 || device == nil {
		return
	}
	deviceID := device.ID
	now := time.Now()
	due := s.interval - min(s.interval/10, time.Minute)
	if v, ok := s.lastTried.Load(deviceID); ok && now.Sub(v.(time.Time)) < due {
		return
	}
	s.lastTried.Store(deviceID, now)
	last, err := s.rec.LastChecked(ctx, deviceID)
	if err != nil {
		return
	}
	if now.Sub(last) < due {
		if !last.IsZero() {
			s.lastTried.Store(deviceID, last)
		}
		return
	}
	if open, err := h.jobs.HasOpenJobByCreator(ctx, deviceID, simwatch.CreatedBy); err != nil || open {
		return
	}
	if active, err := h.jobs.ActiveDiscovery(ctx, deviceID); err != nil || active != nil {
		return
	}
	discovered, err := h.params.GetNames(ctx, deviceID)
	if err != nil || discovered == nil {
		return
	}
	paths := simwatch.ReadPaths(discovered.Names)
	if len(paths) == 0 {
		return
	}
	if _, err := h.jobs.Create(ctx, deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, simwatch.CreatedBy); err != nil {
		h.logger.Error("failed to queue SIM watch read", "err", err, "device_id", deviceID)
	}
}

// simChange is a judged SIM reply, kept for the event published after the
// cache write.
type simChange struct {
	Verdict simwatch.Verdict
	Cur     simwatch.Identity
	Prev    simwatch.Identity
}

// judgeSIM runs before the cache is overwritten, because afterwards the old
// SIM can no longer be compared. The verdict is saved first. skipIdentity is
// true when it could not be (or the cache could not be read): the caller then
// leaves the identity paths out of the cache write, so the next read judges
// the same swap again instead of losing it. No identifier is logged.
func (h *handler) judgeSIM(ctx context.Context, deviceID string, list []cwmp.ParameterValueStruct) (change *simChange, skipIdentity bool) {
	s := h.simWatch
	if s == nil || s.rec == nil {
		return nil, false
	}
	values := make(map[string]string, len(list))
	for _, p := range list {
		if simwatch.IsIdentityPath(p.Name) {
			values[p.Name] = p.Value
		}
	}
	if len(values) == 0 {
		return nil, false
	}
	cache, err := h.params.Get(ctx, deviceID)
	if err != nil {
		h.logger.Error("SIM watch could not read the parameter cache", "err", err, "device_id", deviceID)
		return nil, true
	}
	cachedValues := make(map[string]string)
	for path, v := range cache {
		if simwatch.IsIdentityPath(path) {
			cachedValues[path] = v.Value
		}
	}
	cached, incoming := simwatch.ExtractIdentity(cachedValues), simwatch.ExtractIdentity(values)
	var allowed func(string) bool
	if h.onboarding != nil {
		plan, err := h.onboarding.PlanForDevice(ctx, deviceID)
		if err != nil {
			h.logger.Error("SIM watch could not load the plan", "err", err, "device_id", deviceID)
			return nil, true
		}
		if plan != nil && len(plan.AllowedPLMNs) > 0 {
			allowed = plan.AllowsIMSI
		}
	}
	verdict := simwatch.Judge(cached, incoming, allowed)
	cur := simwatch.Merge(cached, incoming)
	if err := s.rec.Record(ctx, deviceID, verdict, cur, cached, time.Now().UTC()); err != nil {
		h.logger.Error("SIM watch could not save the verdict", "err", err, "device_id", deviceID, "verdict", string(verdict.Kind))
		return nil, true
	}
	return &simChange{Verdict: verdict, Cur: cur, Prev: cached}, false
}

// publishSIMChange tells subscribers about a notice or alert verdict.
func (h *handler) publishSIMChange(ctx context.Context, deviceID string, c *simChange, at time.Time) {
}
