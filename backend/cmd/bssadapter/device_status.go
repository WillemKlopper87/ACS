package main

import (
	"errors"
	"net/http"

	"acs/internal/bss"
)

// getMappedDeviceStatus answers what a support desk asks of a customer's
// router (bss-contract 2.1.0): how long it has been up, how many devices
// are on it and, on a cellular router, its signal. It reads the ACS's
// parameter cache and never contacts the CPE: the values are as old as
// `read_at`, and a REFRESH_STATUS order asks the CPE to report again.
// Only these values leave: no SSID, no credential, no parameter path.
func (h *handler) getMappedDeviceStatus(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("account_id")
	if !h.authorizeBSSAccount(w, r, bss.ScopeTMFRead, accountID) {
		return
	}
	role := roleOrDefault(r.URL.Query().Get("role"))
	mapping, err := h.mappings.ActiveDeviceForAccount(r.Context(), accountID, role)
	if errors.Is(err, bss.ErrNoDeviceForRole) {
		writeError(w, http.StatusNotFound, "ErrDeviceNotMapped", "no active device is assigned to this account in the requested role")
		return
	}
	if err != nil {
		h.logger.Error("failed to resolve device status", "err", err, "account_id", accountID)
		writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		return
	}
	fail := func(err error, what string) bool {
		switch {
		case err == nil:
			return false
		case errors.Is(err, bss.ErrACSUnreachable):
			writeError(w, http.StatusBadGateway, "ErrACSUnreachable", "the underlying ACS engine is unreachable")
		case errors.Is(err, bss.ErrDeviceLookupNotFound):
			writeError(w, http.StatusNotFound, "ErrDeviceNotFound", "the mapped device no longer exists")
		default:
			h.logger.Error("failed to load "+what, "err", err, "device_id", mapping.DeviceID)
			writeError(w, http.StatusInternalServerError, "ErrInternal", "internal error")
		}
		return true
	}
	device, err := h.acs.GetDevice(r.Context(), mapping.DeviceID)
	if fail(err, "device") {
		return
	}
	names, err := h.acs.GetParameterNames(r.Context(), mapping.DeviceID)
	if fail(err, "discovered parameter names") {
		return
	}
	values := map[string]bss.CachedParameter{}
	if paths := bss.StatusPaths(device.DataModelRoot, names); len(paths) > 0 {
		values, err = h.acs.GetParameters(r.Context(), mapping.DeviceID, paths)
		if fail(err, "status parameters") {
			return
		}
	}
	writeJSON(w, http.StatusOK, bss.BuildDeviceStatus(accountID, mapping.DeviceID, role, values))
}
