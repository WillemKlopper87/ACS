// Customizable fleet dashboard (admin-platform backlog). One combined
// endpoint (getDashboard) computes every widget's data in one round trip
// — a dashboard renders several widgets at once, and an operator with a
// slow connection shouldn't pay for N separate requests just because the
// backend happens to compute each number from a different repository.
// Layout persistence (which widgets, what order) is a separate small
// endpoint pair since it changes on a completely different cadence (once
// in a while vs. every dashboard load).
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"acs/internal/dashboard"
	"acs/internal/parameters"
	"acs/internal/rollout"
)

type alarm struct {
	Severity string `json:"severity"` // "critical" | "warning"
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

// computeAlarms derives a small set of real, computed signals — not a
// general rules engine, just the handful of conditions this ACS already
// has the data to detect. Each one only appears if it's actually true.
func computeAlarms(informRecency map[string]int, jobSuccessRatePct float64, jobTotal int, outdatedFirmwareCount int, blockedRollouts int) []alarm {
	var out []alarm

	total := 0
	for _, n := range informRecency {
		total += n
	}
	stale := informRecency["stale"] + informRecency["never"]
	if total > 0 && stale > 0 {
		pct := float64(stale) / float64(total) * 100
		severity := "warning"
		if pct > 20 {
			severity = "critical"
		}
		out = append(out, alarm{
			Severity: severity,
			Title:    "Devices not checking in",
			Detail:   fmt.Sprintf("%d of %d devices (%.0f%%) haven't Informed in 24h+ or ever", stale, total, pct),
		})
	}

	if jobTotal > 0 && jobSuccessRatePct < 90 {
		severity := "warning"
		if jobSuccessRatePct < 70 {
			severity = "critical"
		}
		out = append(out, alarm{
			Severity: severity,
			Title:    "Elevated job failure rate",
			Detail:   fmt.Sprintf("%.1f%% job success rate over the last 24h (%d jobs)", jobSuccessRatePct, jobTotal),
		})
	}

	if outdatedFirmwareCount > 0 {
		out = append(out, alarm{
			Severity: "warning",
			Title:    "Firmware upgrades available",
			Detail:   fmt.Sprintf("%d devices are behind the latest known firmware for their model", outdatedFirmwareCount),
		})
	}

	if blockedRollouts > 0 {
		out = append(out, alarm{
			Severity: "critical",
			Title:    "Rollout blocked",
			Detail:   fmt.Sprintf("%d firmware rollout(s) blocked on failure rate — needs operator review", blockedRollouts),
		})
	}

	// Most-severe first, so the top of the widget is never a "warning"
	// while a "critical" sits below the fold.
	sort.Slice(out, func(i, j int) bool { return out[i].Severity == "critical" && out[j].Severity != "critical" })
	return out
}

// getDashboard computes every widget's data for the calling operator's
// scope in one response.
func (h *handler) getDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	customerIDs, scoped := h.deviceScope(r)

	byStatus, err := h.devices.CountByOnlineStatus(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count devices by status", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byReachability, err := h.devices.CountByReachability(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count devices by reachability", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	informRecency, err := h.devices.InformRecencyBuckets(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to bucket inform recency", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byCustomer, err := h.devices.CountByCustomer(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count devices by customer", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byManufacturer, err := h.devices.CountByManufacturer(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count devices by manufacturer", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byRegion, err := h.tenancy.CountByRegion(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count devices by region", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byProject, err := h.tenancy.CountByProject(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to count devices by project", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	jobStats, err := h.jobs.StatusCountsSince(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		h.logger.Error("failed to count job statuses", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	jobTotal := 0
	for _, n := range jobStats {
		jobTotal += n
	}
	successRate := 0.0
	if jobTotal > 0 {
		successRate = float64(jobStats["SUCCESS"]) / float64(jobTotal) * 100
	}

	// Firmware upgrade detection: compare each device's cached
	// SoftwareVersion against the latest known version for its
	// vendor+model. A device with no cached version, or no matching
	// firmware image on file, is skipped — "unknown" isn't "outdated".
	versions, err := h.devices.SoftwareVersionsByModel(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to read device software versions", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	latest, err := h.firmware.LatestVersions(ctx)
	if err != nil {
		h.logger.Error("failed to load latest firmware versions", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	upToDate, outdated, unknown := 0, 0, 0
	for _, v := range versions {
		if v.SoftwareVersion == "" {
			unknown++
			continue
		}
		want, ok := latest[v.Manufacturer+"|"+v.ProductClass]
		if !ok {
			unknown++
			continue
		}
		if want == v.SoftwareVersion {
			upToDate++
		} else {
			outdated++
		}
	}

	// Blocked rollouts aren't scoped to the operator's devices (rollouts
	// span the fleet by model filter, not by customer) — this is a
	// fleet-operations signal every operator with rollout visibility
	// should see, same reasoning as job stats above.
	rollouts, err := h.rollouts.List(ctx)
	if err != nil {
		h.logger.Error("failed to list rollouts", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	blockedRollouts := 0
	for _, ro := range rollouts {
		if ro.Status == rollout.StatusBlocked {
			blockedRollouts++
		}
	}

	temps, err := h.params.TemperatureReadings(ctx, customerIDs, scoped)
	if err != nil {
		h.logger.Error("failed to read temperature readings", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	alarms := computeAlarms(informRecency, successRate, jobTotal, outdated, blockedRollouts)
	if alarms == nil {
		// computeAlarms returns a nil slice when no condition fires (the
		// common case on a small/empty fleet) — encodes as JSON null,
		// which crashes the frontend's `alarms.length` check.
		alarms = []alarm{}
	}
	if temps == nil {
		temps = []parameters.TemperatureReading{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"devices_by_status":       byStatus,
		"devices_by_reachability": byReachability,
		"inform_recency":          informRecency,
		"group_by": map[string]any{
			"customer":     byCustomer,
			"region":       byRegion,
			"project":      byProject,
			"manufacturer": byManufacturer,
		},
		"jobs_last_24h_total":  jobTotal,
		"job_success_rate_pct": successRate,
		"firmware": map[string]any{
			"up_to_date": upToDate, "outdated": outdated, "unknown": unknown,
		},
		"alarms":       alarms,
		"temperature":  temps,
		"scoped":       scoped,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *handler) getDashboardLayout(w http.ResponseWriter, r *http.Request) {
	op, ok := h.currentOperatorID(r)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"widgets": dashboard.DefaultWidgets})
		return
	}
	widgets, err := h.dashboards.Layout(r.Context(), op)
	if err != nil {
		h.logger.Error("failed to load dashboard layout", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"widgets": widgets})
}

func (h *handler) setDashboardLayout(w http.ResponseWriter, r *http.Request) {
	op, ok := h.currentOperatorID(r)
	if !ok {
		http.Error(w, "no authenticated operator to save a layout for", http.StatusUnauthorized)
		return
	}
	var req struct {
		Widgets []dashboard.Widget `json:"widgets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := h.dashboards.SaveLayout(r.Context(), op, req.Widgets); err != nil {
		h.logger.Error("failed to save dashboard layout", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// currentOperatorID resolves the calling JWT's username into the
// operators.id dashboard_layouts is keyed by — mirrors deviceScope's own
// username->ID lookup, since JWT claims only carry the username.
func (h *handler) currentOperatorID(r *http.Request) (string, bool) {
	if len(h.jwtSecret) == 0 {
		return "", false
	}
	claims, ok := operatorClaims(r.Context())
	if !ok {
		return "", false
	}
	op, err := h.operators.ByUsername(r.Context(), claims.Subject)
	if err != nil {
		return "", false
	}
	return op.ID, true
}
