// Excel reporting (admin-platform backlog): device location metadata plus
// the fleet/region/customer/project-filterable .xlsx export.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/xuri/excelize/v2"
)

type updateLocationRequest struct {
	Location string `json:"location"`
}

func (h *handler) updateDeviceLocation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateLocationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := h.devices.UpdateLocation(r.Context(), id, req.Location); err != nil {
		h.logger.Error("failed to update device location", "err", err, "device_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_id": id, "location": req.Location})
}

var reportColumns = []string{
	"Serial Number", "Manufacturer", "Model", "MAC Address", "Status",
	"Firmware Version", "Current SSID", "Location", "Customer", "Region",
}

// exportDevicesExcel streams a real .xlsx workbook — device status,
// firmware version, current SSID, location, and identity (serial/model/
// MAC), per the user's report spec, filterable to a region/customer/
// project on top of the calling operator's own multi-tenancy scope
// (always applied, same as every other device read in this app).
func (h *handler) exportDevicesExcel(w http.ResponseWriter, r *http.Request) {
	customerIDs, scoped := h.deviceScope(r)

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
		values := []any{row.SerialNumber, row.Manufacturer, row.ProductClass, row.MACAddress, row.OnlineStatus,
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
