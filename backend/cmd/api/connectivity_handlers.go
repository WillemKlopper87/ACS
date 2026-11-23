package main

import (
	"net/http"
	"strconv"
	"time"
)

const (
	defaultConnectivityHours = 24 * 7
	maxConnectivityHours     = 24 * 30 // the default cwmp_sessions retention
)

// getDeviceConnectivity serves a device's availability over a window: the
// stretches in which it stopped reporting to the ACS, and whether it came back
// with a restart code. It is derived from the Informs the ACS recorded, so it
// reaches back as far as ACS_RETENTION_SESSIONS_DAYS.
func (h *handler) getDeviceConnectivity(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	hours := defaultConnectivityHours
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxConnectivityHours {
			http.Error(w, "hours must be a whole number from 1 to "+strconv.Itoa(maxConnectivityHours), http.StatusBadRequest)
			return
		}
		hours = n
	}
	to := time.Now().UTC()
	res, err := h.connectivity.ForDevice(r.Context(), id, to.Add(-time.Duration(hours)*time.Hour), to)
	if err != nil {
		h.logger.Error("failed to read device connectivity", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
