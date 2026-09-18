package main

import (
	"errors"
	"net/http"
	"time"

	"acs/internal/bss"

	"github.com/google/uuid"
)

// getReconciliation compares this device's BSS order history against its
// current ACS-reported parameter state (design: BSS/OSS reconciliation --
// "does the device actually have what we last told it to have"). It is
// read-only: nothing here queues a corrective order. A confirmed drift is
// something an operator or a follow-up automated order decides what to do
// about, not something this endpoint silently fixes.
func (h *handler) getReconciliation(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("device_id")
	if _, err := uuid.Parse(deviceID); err != nil {
		writeError(w, http.StatusNotFound, "ErrNotFound", "no BSS order history for that device")
		return
	}

	orders, err := h.mappings.OrdersForDevice(r.Context(), deviceID, 0)
	if err != nil {
		h.logger.Error("failed to load orders for reconciliation", "err", err, "device_id", deviceID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	if len(orders) == 0 {
		// No account to authorize against and nothing to reconcile --
		// report exactly like an unknown device rather than 200-with-an-
		// empty-list, so this can't be used to probe device ids that
		// exist but were never BSS-managed.
		writeError(w, http.StatusNotFound, "ErrNotFound", "no BSS order history for that device")
		return
	}
	if !h.authorizeBSSAccount(w, r, orders[0].AccountID) {
		return
	}

	confirmed := make(map[string]bool)
	for _, commandKey := range bss.ReconciliationCommandKeys(orders) {
		status, err := h.acs.GetJobStatus(r.Context(), commandKey)
		if err != nil {
			if !errors.Is(err, bss.ErrJobNotFound) {
				h.logger.Warn("failed to check job status for reconciliation", "err", err, "command_key", commandKey)
			}
			continue // unconfirmed is the safe default for a job this couldn't check
		}
		confirmed[commandKey] = status.Status == "SUCCESS"
	}

	paths := bss.ReconciliationPaths(orders)
	current, err := h.acs.GetParameters(r.Context(), deviceID, paths)
	if err != nil {
		h.logger.Error("failed to read device parameters for reconciliation", "err", err, "device_id", deviceID)
		writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
		return
	}

	writeJSON(w, http.StatusOK, bss.ComputeReconciliation(deviceID, orders, confirmed, current, time.Now().UTC()))
}
