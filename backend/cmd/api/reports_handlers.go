// Excel reporting (admin-platform backlog): device location metadata plus
// the fleet/region/customer/project-filterable .xlsx export.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// maxDeviceLabelRunes bounds the operator-chosen device name. Counted in
// runes, not bytes, so a non-Latin label is not truncated at a third of
// its apparent length.
const maxDeviceLabelRunes = 120

type updateLabelRequest struct {
	Label string `json:"label"`
}

// normalizeDeviceLabel trims and validates a device name. An empty result
// clears the label — the same "send nothing to remove it" shape the tags
// and location endpoints already use.
func normalizeDeviceLabel(label string) (string, error) {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return "", nil
	}
	for _, r := range trimmed {
		// Rejects NUL (which Postgres cannot store in a text column at all)
		// and newlines/tabs, which would break every single-line rendering
		// of the name.
		if r < 0x20 || r == 0x7f {
			return "", errors.New("label cannot contain control characters")
		}
	}
	if utf8.RuneCountInString(trimmed) > maxDeviceLabelRunes {
		return "", fmt.Errorf("label cannot be longer than %d characters", maxDeviceLabelRunes)
	}
	return trimmed, nil
}

func (h *handler) updateDeviceLabel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	var req updateLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	label, err := normalizeDeviceLabel(req.Label)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.devices.UpdateLabel(r.Context(), id, label); err != nil {
		h.logger.Error("failed to update device label", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_id": id, "label": label})
}

type updateLocationRequest struct {
	Location  string   `json:"location"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

// validateCoordinates rejects a half-specified or impossible fix. The
// comparisons are written as a positive range test rather than
// `lat > 90 || lat < -90` so that NaN — which compares false against
// everything — is rejected too instead of reaching the database.
func validateCoordinates(latitude, longitude *float64) error {
	if (latitude == nil) != (longitude == nil) {
		return errors.New("latitude and longitude must be provided together")
	}
	if latitude == nil {
		return nil
	}
	if !(*latitude >= -90 && *latitude <= 90) {
		return errors.New("latitude must be between -90 and 90")
	}
	if !(*longitude >= -180 && *longitude <= 180) {
		return errors.New("longitude must be between -180 and 180")
	}
	return nil
}

func (h *handler) updateDeviceLocation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.getScopedDevice(w, r, id); !ok {
		return
	}
	var req updateLocationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := validateCoordinates(req.Latitude, req.Longitude); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.devices.UpdateLocation(r.Context(), id, req.Location, req.Latitude, req.Longitude); err != nil {
		h.logger.Error("failed to update device location", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id": id, "location": req.Location,
		"latitude": req.Latitude, "longitude": req.Longitude,
	})
}

var reportColumns = []string{
	"Serial Number", "Name", "Manufacturer", "Model", "MAC Address", "Status",
	"Firmware Version", "Current SSID", "Location", "Customer", "Region",
}

// exportDevicesExcel streams a real .xlsx workbook — device status,
// firmware version, current SSID, location, and identity (serial/model/
// MAC), per the user's report spec, filterable to a region/customer/
// project on top of the calling operator's own multi-tenancy scope
// (always applied, same as every other device read in this app).
func (h *handler) exportDevicesExcel(w http.ResponseWriter, r *http.Request) {
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var filterCustomer, filterRegion, filterProject *string
	if v := r.URL.Query().Get("customer_id"); v != "" {
		filterCustomer = &v
	}
	if v := r.URL.Query().Get("region_id"); v != "" {
		filterRegion = &v
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		filterProject = &v
	}

	rows, err := h.devices.ReportRows(r.Context(), customerIDs, scoped, filterCustomer, filterRegion, filterProject)
	if err != nil {
		h.logger.Error("failed to query report rows", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	f := excelize.NewFile()
	defer f.Close()
	const sheet = "Devices"
	f.SetSheetName("Sheet1", sheet)

	headerStyle, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Color: []string{"#E7ECF5"}, Pattern: 1}})
	for i, col := range reportColumns {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, col)
		f.SetCellStyle(sheet, cell, cell, headerStyle)
	}

	for i, row := range rows {
		rowNum := i + 2
		values := []any{row.SerialNumber, row.Label, row.Manufacturer, row.ProductClass, row.MACAddress, row.OnlineStatus,
			row.SoftwareVersion, row.SSID, row.Location, row.CustomerName, row.RegionName}
		for c, v := range values {
			cell, _ := excelize.CoordinatesToCellName(c+1, rowNum)
			f.SetCellValue(sheet, cell, v)
		}
	}
	for i := range reportColumns {
		col, _ := excelize.ColumnNumberToName(i + 1)
		f.SetColWidth(sheet, col, col, 18)
	}

	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "DeviceReportExported", map[string]any{
		"row_count": len(rows), "customer_id": filterCustomer, "region_id": filterRegion, "project_id": filterProject,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}

	filename := fmt.Sprintf("acs-devices-%s.xlsx", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	if err := f.Write(w); err != nil {
		h.logger.Error("failed to write xlsx response", "err", err)
	}
}
