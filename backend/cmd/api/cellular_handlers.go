package main

import (
	"net/http"
	"time"

	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"acs/internal/parameters"
)

// cellularCapabilityFields is deliberately an explicit, stable vocabulary.
// It prevents a discovered vendor tree from becoming an unbounded northbound
// API and makes the response safe to consume across device vendors.
var cellularCapabilityFields = []adapters.CellularField{
	adapters.CellularStatus, adapters.CellularAccessPoint,
	adapters.CellularSIMStatus, adapters.CellularIMSI, adapters.CellularICCID,
	adapters.CellularIMEI, adapters.CellularOperator, adapters.CellularCellID,
	adapters.CellularRAT, adapters.CellularBand, adapters.CellularChannel,
	adapters.CellularRSSI, adapters.CellularRSRP, adapters.CellularRSRQ,
	adapters.CellularBytesSent, adapters.CellularBytesReceived,
}

type cellularCapabilityResolution struct {
	Path       string `json:"path"`
	Standard   bool   `json:"standard"`
	Discovered bool   `json:"discovered"`
}

type cellularCapabilitiesResponse struct {
	DeviceID      string                                  `json:"device_id"`
	DataModelRoot string                                  `json:"data_model_root"`
	Profile       cellularProfileProvenance               `json:"profile"`
	DiscoveredAt  *string                                 `json:"discovered_at"`
	Resolutions   map[string]cellularCapabilityResolution `json:"resolutions"`
	Values        map[string]cellularTelemetryValue       `json:"values"`
}

type cellularTelemetryValue struct {
	Path      string `json:"path"`
	Value     any    `json:"value"`
	Raw       string `json:"raw"`
	Unit      string `json:"unit,omitempty"`
	UpdatedAt string `json:"updated_at"`
	Source    string `json:"source"`
}

type cellularProfileProvenance struct {
	ID         *string `json:"id"`
	MatchedBy  *string `json:"matched_by"`
	Qualified  bool    `json:"qualified"`
	Evidence   any     `json:"evidence"`
	AssignedAt *string `json:"assigned_at"`
}

func buildCellularCapabilitiesResponse(device *devices.Device, discovered *parameters.DiscoveredNames, cached map[string]parameters.CachedValue) cellularCapabilitiesResponse {
	var discoveredNames map[string]bool
	var discoveredAt *string
	if discovered != nil {
		discoveredNames = discovered.Names
		value := discovered.DiscoveredAt.Format(time.RFC3339)
		discoveredAt = &value
	}
	profile := cellularProfileProvenance{Qualified: device.ProfileQualified, Evidence: map[string]any{}}
	profile.ID, profile.MatchedBy = device.ProfileID, device.ProfileMatchedBy
	if len(device.ProfileEvidence) > 0 {
		profile.Evidence = device.ProfileEvidence
	}
	if device.ProfileAssignedAt != nil {
		value := device.ProfileAssignedAt.Format(time.RFC3339)
		profile.AssignedAt = &value
	}
	resolutions := make(map[string]cellularCapabilityResolution, len(cellularCapabilityFields))
	values := make(map[string]cellularTelemetryValue)
	for _, field := range cellularCapabilityFields {
		resolution, ok := adapters.ResolveCellularReadPath(device.DataModelRoot, field, "1", discoveredNames)
		if !ok {
			continue
		}
		resolutions[string(field)] = cellularCapabilityResolution{
			Path: resolution.Path, Standard: resolution.Standard, Discovered: resolution.Discovered,
		}
		if value, present := cached[resolution.Path]; present {
			normalized := adapters.NormalizeCellularValue(value.Value)
			values[string(field)] = cellularTelemetryValue{Path: resolution.Path, Value: normalized.Value, Raw: value.Value, UpdatedAt: value.UpdatedAt.Format(time.RFC3339), Source: value.Source}
		}
	}
	return cellularCapabilitiesResponse{
		DeviceID: device.ID, DataModelRoot: device.DataModelRoot, Profile: profile,
		DiscoveredAt: discoveredAt, Resolutions: resolutions, Values: values,
	}
}

// getCellularCapabilities exposes read-only, evidence-backed canonical paths.
// It does not poll the CPE and does not imply that any path is writable.
func (h *handler) getCellularCapabilities(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	discovered, err := h.params.GetNames(r.Context(), device.ID)
	if err != nil {
		h.logger.Error("failed to read discovered cellular capabilities", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cached, err := h.params.Get(r.Context(), device.ID)
	if err != nil {
		h.logger.Error("failed to read cached cellular telemetry", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, buildCellularCapabilitiesResponse(device, discovered, cached))
}
