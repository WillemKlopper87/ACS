// Bulk device import (admin-platform backlog: "device can be added one by
// one, or in bulk through xml/csv/json"). Pre-registers device rows ahead
// of their first real Inform (devices.Repository.PreRegister) — when the
// physical device eventually connects, UpsertFromInform's ON CONFLICT
// match enriches this same row rather than creating a duplicate, so
// pre-provisioning with a customer assignment sticks across that handoff.
package main

import (
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type importRow struct {
	Manufacturer string   `json:"manufacturer" xml:"manufacturer"`
	OUI          string   `json:"oui" xml:"oui"`
	ProductClass string   `json:"product_class" xml:"product_class"`
	SerialNumber string   `json:"serial_number" xml:"serial_number"`
	CustomerID   string   `json:"customer_id,omitempty" xml:"customer_id,omitempty"`
	Tags         []string `json:"tags,omitempty" xml:"tags>tag,omitempty"`
}

type importRowResult struct {
	SerialNumber string `json:"serial_number"`
	Status       string `json:"status"` // "created_or_updated" | "error"
	Error        string `json:"error,omitempty"`
}

// parseImportBody dispatches on the ?format= query param — kept separate
// from the HTTP handler so each format's parsing can be tested in
// isolation without a real request.
func parseImportBody(format string, body io.Reader) ([]importRow, error) {
	switch format {
	case "json":
		var rows []importRow
		if err := json.NewDecoder(body).Decode(&rows); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return rows, nil

	case "xml":
		var doc struct {
			Devices []importRow `xml:"device"`
		}
		if err := xml.NewDecoder(body).Decode(&doc); err != nil {
			return nil, fmt.Errorf("invalid XML: %w", err)
		}
		return doc.Devices, nil

	case "csv":
		reader := csv.NewReader(body)
		header, err := reader.Read()
		if err != nil {
			return nil, fmt.Errorf("invalid CSV (no header row): %w", err)
		}
		col := make(map[string]int, len(header))
		for i, h := range header {
			col[strings.TrimSpace(strings.ToLower(h))] = i
		}
		required := []string{"manufacturer", "oui", "product_class", "serial_number"}
		for _, c := range required {
			if _, ok := col[c]; !ok {
				return nil, fmt.Errorf("CSV header missing required column %q", c)
			}
		}

		var rows []importRow
		for {
			rec, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("invalid CSV row: %w", err)
			}
			row := importRow{
				Manufacturer: rec[col["manufacturer"]],
				OUI:          rec[col["oui"]],
				ProductClass: rec[col["product_class"]],
				SerialNumber: rec[col["serial_number"]],
			}
			if i, ok := col["customer_id"]; ok && i < len(rec) {
				row.CustomerID = rec[i]
			}
			if i, ok := col["tags"]; ok && i < len(rec) && rec[i] != "" {
				row.Tags = strings.Split(rec[i], ";")
			}
			rows = append(rows, row)
		}
		return rows, nil

	default:
		return nil, fmt.Errorf(`format must be "json", "csv", or "xml"`)
	}
}

// importDevices is the REST entry point — POST /devices/import?format=json|csv|xml.
func (h *handler) importDevices(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	rows, err := parseImportBody(format, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "no rows to import", http.StatusBadRequest)
		return
	}
	const maxImportRows = 5000
	if len(rows) > maxImportRows {
		http.Error(w, fmt.Sprintf("too many rows (%d), max %d per import", len(rows), maxImportRows), http.StatusBadRequest)
		return
	}

	results := make([]importRowResult, 0, len(rows))
	created := 0
	for _, row := range rows {
		if row.OUI == "" || row.SerialNumber == "" {
			results = append(results, importRowResult{SerialNumber: row.SerialNumber, Status: "error", Error: "oui and serial_number are required"})
			continue
		}
		ouiSerial := row.OUI + "+" + row.ProductClass + "+" + row.SerialNumber
		var customerID *string
		if row.CustomerID != "" {
			customerID = &row.CustomerID
		}
		if _, err := h.devices.PreRegister(r.Context(), ouiSerial, row.Manufacturer, row.OUI, row.ProductClass, row.SerialNumber, customerID, row.Tags); err != nil {
			h.logger.Error("failed to pre-register device", "err", err, "oui_serial", ouiSerial)
			results = append(results, importRowResult{SerialNumber: row.SerialNumber, Status: "error", Error: "internal error"})
			continue
		}
		created++
		results = append(results, importRowResult{SerialNumber: row.SerialNumber, Status: "created_or_updated"})
	}

	actor := operatorFromRequest(r)
	if err := h.auditor.Record(r.Context(), actor, "", "DevicesBulkImported", map[string]any{
		"format": format, "total_rows": len(rows), "succeeded": created,
	}); err != nil {
		h.logger.Error("failed to write audit record", "err", err)
	}
	h.logger.Info("bulk device import completed", "format", format, "total_rows", len(rows), "succeeded", created, "imported_by", actor)

	writeJSON(w, http.StatusOK, map[string]any{
		"total_rows": len(rows), "succeeded": created, "failed": len(rows) - created, "results": results,
	})
}
