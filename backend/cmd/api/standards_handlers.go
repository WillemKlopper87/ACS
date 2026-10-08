package main

import (
	"acs/internal/datamodel"
	"acs/internal/devices"
	"acs/internal/devices/adapters"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (h *handler) exportDiscovery(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	snapshots, err := h.params.DiscoverySnapshots(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read discovery evidence", 500)
		return
	}
	// Only names, access metadata and identity are exported. Cached values can
	// contain passwords and are deliberately excluded from discovery artifacts.
	writeJSON(w, 200, map[string]any{"device_id": device.ID, "snapshots": snapshots})
}

func dictionaryForRequest(r *http.Request) (*datamodel.Dictionary, error) {
	protocol := r.URL.Query().Get("protocol")
	if protocol == "" {
		protocol = "cwmp"
	}
	return datamodel.Load(protocol, r.URL.Query().Get("version"))
}

func (h *handler) getStandardDictionary(w http.ResponseWriter, r *http.Request) {
	dictionary, err := dictionaryForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	path := r.URL.Query().Get("path")
	if path != "" {
		entry, ok := dictionary.Lookup(path)
		if !ok {
			http.Error(w, "path is not in this bundled dictionary", 404)
			return
		}
		writeJSON(w, 200, entry)
		return
	}
	writeJSON(w, 200, map[string]any{"dictionary": dictionary, "entry_count": len(dictionary.Entries), "versions": []string{"2.20", "2.21"}, "scope": "standard path and scalar syntax checks; device discovery remains authoritative; not a certification"})
}

type pathAssessment struct {
	Path           string           `json:"path"`
	Writable       bool             `json:"writable"`
	Classification string           `json:"classification"`
	Definition     *datamodel.Entry `json:"definition,omitempty"`
}

func assessPaths(d *datamodel.Dictionary, names map[string]bool) []pathAssessment {
	out := make([]pathAssessment, 0, len(names))
	for path, writable := range names {
		a := pathAssessment{Path: path, Writable: writable, Classification: "unrecognized_in_selected_dictionary"}
		if e, ok := d.Lookup(path); ok {
			a.Classification = "standard"
			a.Definition = &e
		} else {
			for _, part := range strings.Split(path, ".") {
				if strings.HasPrefix(part, "X_") {
					a.Classification = "vendor_extension"
					break
				}
			}
			if strings.HasPrefix(path, "InternetGatewayDevice.") {
				a.Classification = "legacy_tr098"
			}
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (h *handler) getStandardsAssessment(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	dictionary, err := dictionaryForRequest(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	discovered, err := h.params.GetNames(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to load discovery", 500)
		return
	}
	names := map[string]bool{}
	if discovered != nil {
		names = discovered.Names
	}
	writeJSON(w, 200, map[string]any{"dictionary": dictionary, "discovered_at": discoveredAtString(discovered), "paths": assessPaths(dictionary, names), "capabilities": adapters.ResolveStandardCapabilities(names), "bbf_0281": "unverified_identifier", "tp_469": "USP agent conformance test plan; no certification claimed"})
}

func (h *handler) getConnectionDiagnostics(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	discovered, err := h.params.GetNames(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to load discovery", 500)
		return
	}
	agent, err := h.devices.GetUspAgentByDeviceID(r.Context(), device.ID)
	if err != nil && !errors.Is(err, devices.ErrUspAgentNotFound) {
		http.Error(w, "failed to read USP connection", 500)
		return
	}
	recommendations := []string{}
	if device.LastInformAt == nil && agent == nil {
		recommendations = append(recommendations, "No authenticated contact recorded. Check ACS URL, network reachability, TLS trust and device-bound credentials before parameter discovery.")
	}
	if discovered == nil {
		recommendations = append(recommendations, "Queue parameter discovery after authenticated contact.")
	}
	if discovered != nil && discovered.Stale {
		recommendations = append(recommendations, "Firmware changed; previous capability evidence is stale. Automatic rediscovery is queued.")
	}
	if device.ConnectionRequestMode == "PERIODIC_FALLBACK_ONLY" {
		recommendations = append(recommendations, "Use periodic Inform for CWMP behind unreachable NAT; use persistent USP where the device supports it.")
	}
	writeJSON(w, 200, map[string]any{"device_id": device.ID, "last_inform_at": device.LastInformAt, "auth_mode": device.CWMPAuthMode, "data_model_root": device.DataModelRoot, "connection_request_mode": device.ConnectionRequestMode, "last_connection_request_status": device.LastConnectionRequestStatus, "usp": agent, "discovered_at": discoveredAtString(discovered), "recommendations": recommendations, "checks": devices.ConnectionChecks(device, agent, time.Now()), "as_of": time.Now().UTC(), "scope": "recorded connection evidence; unknown checks are not successful probes"})
}

func (h *handler) getWiFiTopology(w http.ResponseWriter, r *http.Request) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	discovered, err := h.params.GetNames(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read discovery", 500)
		return
	}
	cached, err := h.params.Get(r.Context(), device.ID)
	if err != nil {
		http.Error(w, "failed to read topology", 500)
		return
	}
	names := map[string]bool{}
	if discovered != nil {
		names = discovered.Names
	}
	topology := adapters.ProjectWiFiTopology(names, cached)
	writeJSON(w, 200, topology)
}
