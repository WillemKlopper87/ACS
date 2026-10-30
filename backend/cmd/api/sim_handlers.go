package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"acs/internal/operators"
	"acs/internal/parameters"
)

// SIM / subscriber identity view. Field test 2026-10-30 (Nokia FastMile
// 1.2501.00.0798) exposes Device.Cellular.Interface.1.USIM.{IMSI,ICCID,
// MSISDN,Status,PIN,PINCheck}, IMEI/IMEISV, X_ALU-COM_CurrentUsedIMSI and
// X_ALU-COM_ESIM.{Eid,ActiveProfileICCID,CurrentUsedProfileICCID,Status}.
// Identifiers are personal information (POPIA) and RICA-registered
// identity, so GET returns them masked to every role; the full values are
// disclosed only by POST .../sim/reveal to a human superadmin who states a
// reason, and every disclosure is audited (who, when, which fields, why —
// never the values). SIM PIN/PINCheck are never returned to anyone.

const minSIMRevealReason = 10

type simField struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Value     string `json:"value"`
	Masked    bool   `json:"masked"`
	UpdatedAt string `json:"updated_at"`
	Source    string `json:"source"`
}

type simView struct {
	DeviceID     string     `json:"device_id"`
	Supported    bool       `json:"supported"`
	Reason       string     `json:"reason,omitempty"`
	Revealed     bool       `json:"revealed"`
	Identifiers  []simField `json:"identifiers"`
	Context      []simField `json:"context"`
	RefreshPaths []string   `json:"refresh_paths"`
}

// simContextLeaf reports non-identifying SIM state shown unmasked: SIM
// status, home network (MCC/MNC/PLMN/SPN) and eSIM status.
func simContextLeaf(path string) bool {
	if !strings.HasPrefix(path, "Device.Cellular.Interface.") {
		return false
	}
	leaf := path[strings.LastIndex(path, ".")+1:]
	switch {
	case strings.Contains(path, ".USIM.") && leaf == "Status":
		return true
	case strings.Contains(path, "ESIM.") && (leaf == "Status" || strings.HasSuffix(leaf, "NumberOfEntries")):
		return true
	case strings.HasSuffix(leaf, "_MCC"), strings.HasSuffix(leaf, "_MNC"), strings.HasSuffix(leaf, "_PLMN"), strings.HasSuffix(leaf, "_SPN"):
		return true
	}
	return false
}

// buildSIMView assembles the view from discovered names and the cache.
// reveal=false masks every identifier.
func buildSIMView(deviceID string, names map[string]bool, cached map[string]parameters.CachedValue, reveal bool) simView {
	view := simView{DeviceID: deviceID, Revealed: reveal, Identifiers: []simField{}, Context: []simField{}, RefreshPaths: []string{}}
	candidates := map[string]bool{}
	for p := range names {
		candidates[p] = true
	}
	for p := range cached {
		candidates[p] = true
	}
	for path := range candidates {
		if !strings.HasPrefix(path, "Device.Cellular.") && !strings.HasPrefix(path, "InternetGatewayDevice.") {
			continue
		}
		if strings.HasSuffix(path, ".") {
			continue
		}
		kind := parameters.SubscriberIdentifierKind(path)
		isContext := kind == "" && simContextLeaf(path)
		if kind == parameters.SubscriberSecret || (kind == "" && !isContext) {
			continue
		}
		v, ok := cached[path]
		if !ok {
			view.RefreshPaths = append(view.RefreshPaths, path)
			continue
		}
		field := simField{Kind: kind, Path: path, Value: v.Value, UpdatedAt: v.UpdatedAt.Format(time.RFC3339), Source: v.Source}
		if isContext {
			field.Kind = "context"
			view.Context = append(view.Context, field)
			continue
		}
		if !reveal {
			field.Value = parameters.MaskSubscriberIdentifier(kind, v.Value)
			field.Masked = field.Value != v.Value
		}
		view.Identifiers = append(view.Identifiers, field)
	}
	sort.Slice(view.Identifiers, func(i, j int) bool { return view.Identifiers[i].Path < view.Identifiers[j].Path })
	sort.Slice(view.Context, func(i, j int) bool { return view.Context[i].Path < view.Context[j].Path })
	sort.Strings(view.RefreshPaths)
	view.Supported = len(view.Identifiers) > 0 || len(view.RefreshPaths) > 0
	if !view.Supported {
		view.Reason = "the device reports no SIM/cellular identifiers (no Device.Cellular. in its discovered tree)"
	}
	return view
}

func (h *handler) loadSIMView(w http.ResponseWriter, r *http.Request, reveal bool) (simView, bool) {
	device, ok := h.getScopedDevice(w, r, r.PathValue("id"))
	if !ok {
		return simView{}, false
	}
	discovered, err := h.params.GetNames(r.Context(), device.ID)
	if err != nil {
		h.logger.Error("failed to read discovered parameter names", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return simView{}, false
	}
	cache, err := h.params.Get(r.Context(), device.ID)
	if err != nil {
		h.logger.Error("failed to read parameter cache", "err", err, "device_id", device.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return simView{}, false
	}
	var names map[string]bool
	if discovered != nil {
		names = discovered.Names
	}
	return buildSIMView(device.ID, names, cache, reveal), true
}

// getSIM returns the masked SIM view (any role with device access).
func (h *handler) getSIM(w http.ResponseWriter, r *http.Request) {
	view, ok := h.loadSIMView(w, r, false)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// canRevealSubscriberIdentity is true only for a human superadmin: not the
// BSS adapter's service token (which carries the superadmin role for its
// narrow route allowlist) and not lab mode without authentication.
func canRevealSubscriberIdentity(r *http.Request) bool {
	claims, ok := operatorClaims(r.Context())
	return ok && claims.Role == operators.RoleSuperAdmin && claims.Subject != "" && claims.Subject != serviceSubject
}

// revealSIM discloses full subscriber identifiers to a superadmin, e.g. for
// RICA subscriber verification or a lawful-interception directive. The
// reason (and optional case/directive reference) is mandatory and audited.
func (h *handler) revealSIM(w http.ResponseWriter, r *http.Request) {
	if !canRevealSubscriberIdentity(r) {
		http.Error(w, "subscriber identity disclosure is restricted to superadmin operators", http.StatusForbidden)
		return
	}
	var req struct {
		Reason    string `json:"reason"`
		Reference string `json:"reference"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	req.Reason, req.Reference = strings.TrimSpace(req.Reason), strings.TrimSpace(req.Reference)
	if len(req.Reason) < minSIMRevealReason {
		http.Error(w, "reason is required (at least 10 characters), e.g. the RICA verification or directive it serves", http.StatusBadRequest)
		return
	}
	view, ok := h.loadSIMView(w, r, true)
	if !ok {
		return
	}
	fields := make([]string, 0, len(view.Identifiers))
	for _, f := range view.Identifiers {
		fields = append(fields, f.Path)
	}
	// Disclosure is refused when it cannot be recorded.
	if err := h.auditor.Record(r.Context(), operatorFromRequest(r), view.DeviceID, "SubscriberIdentityRevealed", map[string]any{
		"fields": fields, "reason": req.Reason, "reference": req.Reference,
	}); err != nil {
		h.logger.Error("failed to write audit record; subscriber identity not disclosed", "err", err, "device_id", view.DeviceID)
		http.Error(w, "audit unavailable; disclosure refused", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, view)
}
