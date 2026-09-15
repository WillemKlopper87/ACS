package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"acs/internal/devices"
	"acs/internal/tmf/common"
	"acs/internal/tmf/resourceinventory"
)

const tmf639MaxLimit = 500

func (h *handler) listTMF639Resources(w http.ResponseWriter, r *http.Request) {
	r, ok := h.prepareTMFRequest(w, r)
	if !ok {
		return
	}
	page, err := common.ParsePage(r.URL.Query(), common.DefaultPageLimit, tmf639MaxLimit)
	if err != nil {
		writeTMFError(w, err)
		return
	}
	fields, err := common.ParseFields(r.URL.Query().Get("fields"), resourceinventory.AllowedFields...)
	if err != nil {
		writeTMFError(w, err)
		return
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		writeTMFInternalError(w)
		return
	}

	result, err := h.devices.ListWindow(r.Context(), devices.WindowParams{
		Offset:      page.Offset,
		Limit:       page.Limit,
		CustomerIDs: customerIDs,
		Scoped:      scoped,
	})
	if err != nil {
		h.logger.Error("failed to list TMF639 resources", "err", err)
		writeTMFInternalError(w)
		return
	}

	enrichment, err := h.tmf639Enrichment(r.Context(), result.Items)
	if err != nil {
		h.logger.Error("failed to enrich TMF639 resources", "err", err)
		writeTMFInternalError(w)
		return
	}
	projector := resourceinventory.NewProjector(tmfNorthboundBaseURL())
	items := make([]map[string]any, 0, len(result.Items))
	for _, device := range result.Items {
		resource, err := projector.ProjectWithEnrichment(device, enrichment[device.ID])
		if err != nil {
			h.logger.Error("failed to project TMF639 resource", "err", err, "device_id", device.ID)
			writeTMFInternalError(w)
			return
		}
		items = append(items, resourceinventory.ProjectFields(resource, fields))
	}

	w.Header().Set("X-Total-Count", strconv.Itoa(result.Total))
	w.Header().Set("X-Result-Count", strconv.Itoa(len(items)))
	writeJSON(w, http.StatusOK, items)
}

func (h *handler) getTMF639Resource(w http.ResponseWriter, r *http.Request) {
	r, ok := h.prepareTMFRequest(w, r)
	if !ok {
		return
	}
	fields, err := common.ParseFields(r.URL.Query().Get("fields"), resourceinventory.AllowedFields...)
	if err != nil {
		writeTMFError(w, err)
		return
	}

	id := r.PathValue("id")
	device, err := h.devices.Get(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeTMFError(w, common.NotFound("resource"))
		return
	}
	if err != nil {
		h.logger.Error("failed to get TMF639 resource device", "err", err, "device_id", id)
		writeTMFInternalError(w)
		return
	}
	customerIDs, scoped, err := h.deviceScope(r)
	if err != nil {
		writeTMFInternalError(w)
		return
	}
	if scoped && !deviceInScope(device.CustomerID, customerIDs) {
		// Deliberately indistinguishable from a missing resource: northbound
		// identifiers must not become a cross-tenant existence oracle.
		writeTMFError(w, common.NotFound("resource"))
		return
	}

	enrichment, err := h.tmf639Enrichment(r.Context(), []devices.Device{*device})
	if err != nil {
		h.logger.Error("failed to enrich TMF639 resource", "err", err, "device_id", device.ID)
		writeTMFInternalError(w)
		return
	}
	resource, err := resourceinventory.NewProjector(tmfNorthboundBaseURL()).ProjectWithEnrichment(*device, enrichment[device.ID])
	if err != nil {
		h.logger.Error("failed to project TMF639 resource", "err", err, "device_id", device.ID)
		writeTMFInternalError(w)
		return
	}
	writeJSON(w, http.StatusOK, resourceinventory.ProjectFields(resource, fields))
}

func (h *handler) tmf639Enrichment(ctx context.Context, page []devices.Device) (map[string]resourceinventory.Enrichment, error) {
	out := make(map[string]resourceinventory.Enrichment, len(page))
	if len(page) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(page))
	for _, device := range page {
		ids = append(ids, device.ID)
	}

	protocols, err := h.devices.ManagementProtocolsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	roles, err := h.bssMappings.ActiveRolesForDevices(ctx, ids)
	if err != nil {
		return nil, err
	}
	versions, err := h.params.InventoryFactsForDevices(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		facts := versions[id]
		out[id] = resourceinventory.Enrichment{
			ManagementProtocols: protocols[id],
			AssignmentRoles:     roles[id],
			SoftwareVersion:     facts.SoftwareVersion,
			HardwareVersion:     facts.HardwareVersion,
		}
	}
	return out, nil
}

func (h *handler) prepareTMFRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if correlationID == "" {
		correlationID = common.NewCorrelationID()
	} else {
		parsed, err := common.ParseCorrelationID(correlationID)
		if err != nil {
			writeTMFError(w, err)
			return nil, false
		}
		correlationID = parsed
	}
	w.Header().Set("X-Correlation-ID", correlationID)
	ctx := common.WithCorrelation(r.Context(), common.Correlation{RequestID: correlationID})
	return r.WithContext(ctx), true
}

func tmfNorthboundBaseURL() string {
	if base := strings.TrimSpace(os.Getenv("ACS_TMF_BASE_URL")); base != "" {
		return base
	}
	// Fixed development fallback rather than request.Host: reflecting an
	// untrusted Host header into href fields would let a caller poison links
	// returned to downstream OSS/BSS systems.
	return "http://localhost:8080"
}

func writeTMFError(w http.ResponseWriter, err error) {
	apiErr := &common.APIError{
		Status:  http.StatusInternalServerError,
		Code:    "INTERNAL_ERROR",
		Reason:  "Internal server error",
		Message: "internal server error",
	}
	var typed *common.APIError
	if errors.As(err, &typed) && typed != nil {
		apiErr = typed
	}
	body := map[string]any{
		"code":    apiErr.Code,
		"reason":  apiErr.Reason,
		"message": apiErr.Message,
	}
	if apiErr.ReferenceError != "" {
		body["referenceError"] = apiErr.ReferenceError
	}
	writeJSON(w, apiErr.Status, body)
}

func writeTMFInternalError(w http.ResponseWriter) {
	writeTMFError(w, errors.New("internal"))
}
