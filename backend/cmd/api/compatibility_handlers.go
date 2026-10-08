package main

import (
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/parameters"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (h *handler) discoverInstances(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	agent, err := h.devices.GetUspAgentByDeviceID(r.Context(), device.ID)
	if err != nil && !errors.Is(err, devices.ErrUspAgentNotFound) {
		http.Error(w, "failed to read USP agent", 500)
		return
	}
	if agent == nil {
		http.Error(w, "instance discovery requires a USP agent", 409)
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil || len(req.Paths) == 0 || len(req.Paths) > 32 {
		http.Error(w, "provide 1..32 object paths", 400)
		return
	}
	for _, path := range req.Paths {
		if !strings.HasPrefix(path, "Device.") || !strings.HasSuffix(path, ".") || strings.ContainsAny(path, "{}*[]() /\\") || len(path) > 512 {
			http.Error(w, "use concrete Device object paths ending in a dot", 400)
			return
		}
	}
	cache, err := h.params.Get(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read firmware", 500)
		return
	}
	job, err := h.jobs.Create(r.Context(), device.ID, jobs.TypeGetParameter, jobs.GetParameterPayload{Paths: req.Paths, Instances: true, Firmware: parameters.Firmware(cache)}, operatorFromRequest(r))
	if err != nil {
		http.Error(w, "failed to queue instances", 500)
		return
	}
	writeJSON(w, 202, map[string]any{"command_key": job.CommandKey, "status": job.Status})
}
func (h *handler) getInstances(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	rows, err := h.params.Instances(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read instance evidence", 500)
		return
	}
	cache, err := h.params.Get(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read firmware", 500)
		return
	}
	writeJSON(w, 200, map[string]any{"firmware": parameters.Firmware(cache), "evidence": rows, "as_of": time.Now().UTC(), "max_age_seconds": 86400})
}
func (h *handler) getCompatibility(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	cache, err := h.params.Get(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read firmware", 500)
		return
	}
	firmware := parameters.Firmware(cache)
	summary, err := h.params.OperationSummary(r.Context(), device.ID, firmware)
	if err != nil {
		http.Error(w, "failed to read operation evidence", 500)
		return
	}
	names, err := h.params.GetNames(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read discovery", 500)
		return
	}
	agent, err := h.devices.GetUspAgentByDeviceID(r.Context(), device.ID)
	if err != nil && !errors.Is(err, devices.ErrUspAgentNotFound) {
		http.Error(w, "failed to read contact", 500)
		return
	}
	snapshots, err := h.params.DiscoverySnapshots(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read snapshots", 500)
		return
	}
	delta := map[string]any{"added": []string{}, "removed": []string{}, "access_changed": []string{}}
	if len(snapshots) > 1 {
		added, removed, changed := []string{}, []string{}, []string{}
		for path, writable := range snapshots[0].Names {
			previous, ok := snapshots[1].Names[path]
			if !ok {
				added = append(added, path)
			} else if previous != writable {
				changed = append(changed, path)
			}
		}
		for path := range snapshots[1].Names {
			if _, ok := snapshots[0].Names[path]; !ok {
				removed = append(removed, path)
			}
		}
		sort.Strings(added)
		sort.Strings(removed)
		sort.Strings(changed)
		delta = map[string]any{"added": added, "removed": removed, "access_changed": changed}
	}
	writeJSON(w, 200, map[string]any{"firmware": firmware, "connected": device.LastInformAt != nil || agent != nil, "discovered": names != nil && !names.Stale && len(names.Names) > 0, "read_tested": firmware != "" && summary["read_tested"], "write_tested": firmware != "" && summary["write_tested"], "firmware_qualified": device.ProfileQualified, "profile_id": device.ProfileID, "qualification_evidence": device.ProfileEvidence, "capability_changes": delta, "scope": "read/write evidence covers returned paths only; firmware qualification requires a physical test record"})
}
