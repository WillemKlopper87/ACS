package main

import (
	"context"
	"sync"
	"time"

	"acs/internal/cwmp"
	"acs/internal/jobs"
	"acs/internal/wifiinsights"
)

// wifiInsightsSampler queues the Wi-Fi insights read on check-ins and
// records its response as a sample (docs/WIFI-MANAGEMENT-SPEC.md §3.3a).
type wifiInsightsSampler struct {
	repo     *wifiinsights.Repository
	interval time.Duration
	// lastTried throttles the per-Inform DB checks to one per interval per
	// device; it is process-local, so a restart costs one extra check.
	lastTried sync.Map
}

// maybeQueueWiFiInsights queues one sampling GET when the device's last
// sample is older than the interval, nothing is pending, and no discovery
// walk is competing for the session.
func (h *handler) maybeQueueWiFiInsights(ctx context.Context, deviceID string) {
	s := h.wifiInsights
	if s == nil || s.repo == nil || s.interval <= 0 {
		return
	}
	now := time.Now()
	if v, ok := s.lastTried.Load(deviceID); ok && now.Sub(v.(time.Time)) < s.interval {
		return
	}
	s.lastTried.Store(deviceID, now)
	if last, err := s.repo.LastSampleAt(ctx, deviceID); err != nil || now.Sub(last) < s.interval {
		return
	}
	if open, err := h.jobs.HasOpenJobByCreator(ctx, deviceID, wifiinsights.CreatedBy); err != nil || open {
		return
	}
	if active, err := h.jobs.ActiveDiscovery(ctx, deviceID); err != nil || active != nil {
		return
	}
	discovered, err := h.params.GetNames(ctx, deviceID)
	if err != nil || discovered == nil {
		return
	}
	paths := wifiinsights.ReadPaths(discovered.Names)
	if len(paths) == 0 {
		return
	}
	if _, err := h.jobs.Create(ctx, deviceID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: paths}, wifiinsights.CreatedBy); err != nil {
		h.logger.Error("failed to queue Wi-Fi insights sample", "err", err, "device_id", deviceID)
	}
}

// recordWiFiInsights stores a sampling response. It is not written to the
// parameter cache: byte counters change on every sample and would flood
// parameter_history.
func (h *handler) recordWiFiInsights(ctx context.Context, deviceID string, list []cwmp.ParameterValueStruct) {
	if h.wifiInsights == nil || h.wifiInsights.repo == nil {
		return
	}
	values := make(map[string]string, len(list))
	for _, p := range list {
		values[p.Name] = p.Value
	}
	var names map[string]bool
	if discovered, err := h.params.GetNames(ctx, deviceID); err == nil && discovered != nil {
		names = discovered.Names
	}
	cache, err := h.params.Get(ctx, deviceID)
	if err != nil {
		h.logger.Error("failed to read parameter cache for Wi-Fi insights", "err", err, "device_id", deviceID)
		return
	}
	snap := wifiinsights.Parse(values, names, cache)
	// A linked mesh satellite is not a client even under a MAC the gateway's
	// EasyMesh table does not carry (field data: the Beacon in Hosts).
	satellites, err := h.devices.ListSatellites(ctx, deviceID, nil, false)
	if err != nil {
		h.logger.Warn("failed to list mesh satellites for Wi-Fi insights", "err", err, "device_id", deviceID)
	}
	for _, sat := range satellites {
		if satCache, err := h.params.Get(ctx, sat.ID); err == nil {
			snap.ExcludeMACs(wifiinsights.SatelliteMACs(satCache))
		}
	}
	if err := h.wifiInsights.repo.Record(ctx, deviceID, time.Now().UTC().Truncate(time.Second), snap, wifiinsights.DefaultSessionGap); err != nil {
		h.logger.Error("failed to record Wi-Fi insights sample", "err", err, "device_id", deviceID)
		return
	}
	wireless, wired, _ := snap.Counts()
	h.logger.Info("Wi-Fi insights sample recorded", "device_id", deviceID, "ssids", len(snap.SSIDs), "wireless_clients", wireless, "wired_clients", wired, "mesh_nodes_excluded", len(snap.MeshMACs))
}
