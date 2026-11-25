package main

import (
	"context"
	"sync"
	"time"

	"acs/internal/cpemetrics"
	"acs/internal/cpemetrics/recorder"
	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/jobs"
)

// cpeMetricsSampler queues the cellular signal read on check-ins and records
// its reply as a sample. It mirrors wifiInsightsSampler: the read is a plain
// GetParameterValues, nothing is written to the CPE.
type cpeMetricsSampler struct {
	repo     *cpemetrics.Repository
	rec      *recorder.Recorder
	interval time.Duration
	// lastTried throttles the per-Inform DB checks to one per interval per
	// device; it is process-local, so a restart costs one extra check.
	lastTried sync.Map
}

// maybeQueueCPEMetrics queues one signal read when the device's last sample
// is older than the interval, nothing is pending, no discovery walk is
// competing for the session, and the device exposes cellular signal paths.
func (h *handler) maybeQueueCPEMetrics(ctx context.Context, device *devices.Device) {
	s := h.cpeMetrics
	if s == nil || s.repo == nil || s.rec == nil || s.interval <= 0 || device == nil {
		return
	}
	deviceID := device.ID
	now := time.Now()
	if v, ok := s.lastTried.Load(deviceID); ok && now.Sub(v.(time.Time)) < s.interval {
		return
	}
	s.lastTried.Store(deviceID, now)
	if last, err := s.repo.LastSampleAt(ctx, deviceID); err != nil || now.Sub(last) < s.interval {
		return
	}
	if open, err := h.jobs.HasOpenJobByCreator(ctx, deviceID, cpemetrics.CreatedBy); err != nil || open {
		return
	}
	if active, err := h.jobs.ActiveDiscovery(ctx, deviceID); err != nil || active != nil {
		return
	}
	discovered, err := h.params.GetNames(ctx, deviceID)
	if err != nil || discovered == nil {
		return
	}
	plan := cpemetrics.PlanReads(device.DataModelRoot, discovered.Names)
	if len(plan) == 0 {
		return
	}
	paths := make([]string, 0, len(plan))
	for _, r := range plan {
		paths = append(paths, r.Path)
	}
	if _, err := h.jobs.Create(ctx, deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, cpemetrics.CreatedBy); err != nil {
		h.logger.Error("failed to queue CPE signal sample", "err", err, "device_id", deviceID)
	}
}

// recordCPEMetrics stores a signal-read reply. It is not written to the
// parameter cache: readings change at every sample and would flood
// parameter_history.
func (h *handler) recordCPEMetrics(ctx context.Context, deviceID string, list []cwmp.ParameterValueStruct) {
	s := h.cpeMetrics
	if s == nil || s.rec == nil {
		return
	}
	count := func(result string) {
		if h.metrics != nil && h.metrics.CPESamplesTotal != nil {
			h.metrics.CPESamplesTotal.WithLabelValues(result).Inc()
		}
	}
	device, err := h.devices.Get(ctx, deviceID)
	if err != nil || device == nil {
		h.logger.Error("failed to load device for CPE signal sample", "err", err, "device_id", deviceID)
		count("failed")
		return
	}
	var names map[string]bool
	if discovered, err := h.params.GetNames(ctx, deviceID); err == nil && discovered != nil {
		names = discovered.Names
	}
	sample, ok := cpemetrics.Parse(cpemetrics.PlanReads(device.DataModelRoot, names), list)
	if !ok {
		count("empty")
		return
	}
	if err := s.rec.Record(ctx, deviceID, time.Now().UTC(), sample); err != nil {
		h.logger.Error("failed to record CPE signal sample", "err", err, "device_id", deviceID)
		count("failed")
		return
	}
	count("recorded")
}
