package main

import (
	"net/http"
	"strconv"
	"time"

	"acs/internal/cpemetrics"
)

const (
	defaultSignalHours = 24 * 7
	maxSignalHours     = 24 * 30
	// signalMaxPoints is how many points a history is averaged down to.
	signalMaxPoints = 500
)

// getDeviceSignalHistory serves a cellular CPE's recorded radio readings over a
// window, averaged to about signalMaxPoints. Read-only.
func (h *handler) getDeviceSignalHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	hours := defaultSignalHours
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxSignalHours {
			http.Error(w, "hours must be a whole number from 1 to "+strconv.Itoa(maxSignalHours), http.StatusBadRequest)
			return
		}
		hours = n
	}
	to := time.Now().UTC()
	res, err := h.cpeMetrics.History(r.Context(), id, to.Add(-time.Duration(hours)*time.Hour), to, signalMaxPoints)
	if err != nil {
		h.logger.Error("failed to read device signal history", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if res.Samples == nil {
		res.Samples = []cpemetrics.Point{}
	}
	writeJSON(w, http.StatusOK, res)
}
