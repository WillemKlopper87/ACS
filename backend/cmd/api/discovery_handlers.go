// Parameter discovery REST surface (nice-to-have backlog). cmd/acs queues
// this automatically on a device's first BOOTSTRAP (see cmd/acs/discovery.go)
// — createParameterDiscovery is the on-demand counterpart, for a device
// that connected before this feature existed, or after a firmware upgrade
// changes what it supports.
package main

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"acs/internal/devices"
	"acs/internal/jobs"
)

// createParameterDiscovery queues a PARAMETER_DISCOVERY job, starting from
// whatever root the device already confirmed (if any) and falling back to
// TR-181 (Device.) first for a device that has never been discovered.
func (h *handler) createParameterDiscovery(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	device, err := h.devices.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to get device", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	root, fallback := "Device.", "InternetGatewayDevice."
	if device.DataModelRoot == devices.DataModelRootIGD1 {
		root, fallback = fallback, root
	}

	job, err := h.jobs.Create(r.Context(), id, jobs.TypeParameterDiscovery,
		jobs.ParameterDiscoveryPayload{Root: root, FallbackRoot: fallback}, operatorFromRequest(r))
	if err != nil {
		h.logger.Error("failed to queue parameter discovery", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), id, "ParameterDiscoveryQueued", map[string]any{"command_key": job.CommandKey, "root": root}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"command_key": job.CommandKey, "status": job.Status})
}

// getParameterNames returns a device's last-discovered parameter tree
// (names + writable flags) — 200 with a null discovered_at if discovery has
// never run for this device, not a 404, since the device itself does exist.
func (h *handler) getParameterNames(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.devices.Get(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		h.logger.Error("failed to get device", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	discovered, err := h.params.GetNames(r.Context(), id)
	if err != nil {
		h.logger.Error("failed to read discovered parameter names", "err", err, "id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if discovered == nil {
		writeJSON(w, http.StatusOK, map[string]any{"names": map[string]bool{}, "discovered_at": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"names": discovered.Names, "discovered_at": discovered.DiscoveredAt.Format(time.RFC3339),
	})
}
