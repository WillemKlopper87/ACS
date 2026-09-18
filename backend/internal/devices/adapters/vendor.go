package adapters

import (
	"sort"
	"strings"
)

// cellularDiagnosticKeywords identify RF/signal-quality telemetry
// parameters within a vendor's catalog — the same parameters a GenieACS
// provision script would hand-pick per vendor to poll after a session
// opens. Deriving the list from the catalog (rather than hardcoding each
// vendor's paths by hand, the way such a script typically does) means
// every path this returns is guaranteed to exist in that vendor's own
// advertised data model.
var cellularDiagnosticKeywords = []string{
	"RSRP", "RSRQ", "SINR", "RSSI", "SNR",
	"CELLID", "PCI", "BAND", "BEAMID", "GNB_ID", "ARFCN",
	"NETWORKTYPE", "SIGNALSTRENGTH",
}

// CellularDiagnosticParams returns the read-only RF/signal-quality
// parameter paths in a catalog — the subset a "refresh cellular
// diagnostics" job reads. Read-only because this is a telemetry poll, not
// a configuration change.
func (c Catalog) CellularDiagnosticParams() []string {
	var out []string
	for _, p := range c.Parameters {
		if p.Access != "readOnly" {
			continue
		}
		leaf := strings.ToUpper(lastPathSegment(p.Path))
		for _, kw := range cellularDiagnosticKeywords {
			if strings.Contains(leaf, kw) {
				out = append(out, p.Path)
				break
			}
		}
	}
	return out
}

// CellularDiagnosticParamsFromNames selects RF diagnostics from a device's
// discovered TR-181 or vendor-extension tree. Unlike a static catalog, this
// works for every firmware variant of a vendor family (and for unknown
// vendors) after discovery. The bool values describe writability, which is
// irrelevant to a read operation; their presence is the capability evidence.
func CellularDiagnosticParamsFromNames(names map[string]bool) []string {
	seen := make(map[string]struct{})
	for path := range names {
		leaf := strings.ToUpper(lastPathSegment(path))
		for _, keyword := range cellularDiagnosticKeywords {
			if strings.Contains(leaf, keyword) {
				seen[path] = struct{}{}
				break
			}
		}
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func lastPathSegment(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

// genericCellularFallback is the standard TR-181 Cellular signal-quality
// path set used when a device's manufacturer doesn't match any known
// vendor catalog.
//
// These sit directly on Device.Cellular.Interface.{i} — verified against
// TR-181 Issue 2.19, whose profile index lists RSSI/RSRP/RSRQ on the
// Interface object and confines Stats. to byte/packet counters. There is
// deliberately no SINR entry: TR-181 does not define one at all, so any
// device reporting SINR does so through a vendor extension, which is the
// per-vendor catalog's job rather than this fallback's.
var genericCellularFallback = []string{
	"Device.Cellular.Interface.1.RSRP",
	"Device.Cellular.Interface.1.RSRQ",
	"Device.Cellular.Interface.1.RSSI",
}

// Registry holds every vendor's parsed catalog, loaded once at startup.
// Read-only after construction, safe for concurrent use.
type Registry struct {
	catalogs []Catalog
}

func NewRegistry() *Registry {
	return &Registry{catalogs: LoadCatalogList()}
}

// ProfileMatch describes how a catalog was selected. Exact model matches are
// qualification evidence; vendor matches are deliberately labelled fallback
// so callers do not mistake a family baseline for a tested device pack.
type ProfileMatch struct {
	Catalog   Catalog
	MatchedBy string // "model", "vendor", or ""
	Qualified bool
}

// MatchProfile selects a profile for a reported manufacturer and ProductClass.
// Model matching is normalized exact matching rather than fuzzy substring
// matching: accepting a near-looking ProductClass could apply unsafe vendor
// extension paths to a different CPE. Manufacturer-only selection remains a
// conservative read-only fallback for pre-discovery diagnostics.
func (r *Registry) MatchProfile(manufacturer, productClass string) ProfileMatch {
	mfr, model := vendorKey(manufacturer), vendorKey(productClass)
	for _, cat := range r.catalogs {
		if vendorKey(cat.Vendor) == "" || !strings.Contains(mfr, vendorKey(cat.Vendor)) {
			continue
		}
		if model != "" && model == vendorKey(cat.Model) {
			return ProfileMatch{Catalog: cat, MatchedBy: "model", Qualified: true}
		}
	}
	for _, cat := range r.catalogs {
		if key := vendorKey(cat.Vendor); key != "" && strings.Contains(mfr, key) {
			return ProfileMatch{Catalog: cat, MatchedBy: "vendor"}
		}
	}
	return ProfileMatch{}
}

// MatchCellularDiagnostics returns the cellular/RF diagnostic parameter
// paths to poll for a device, matched by manufacturer name (case-
// insensitive substring — e.g. a reported manufacturer string of "ZYXEL
// Communications Corp." matches the "zyxel" catalog, mirroring the
// mfr.includes(...) matching a GenieACS provision script uses). Falls
// back to the generic TR-181 Cellular path set if nothing matches.
func (r *Registry) MatchCellularDiagnostics(manufacturer string) (vendor string, paths []string) {
	match := r.MatchProfile(manufacturer, "")
	if match.MatchedBy != "" {
		return match.Catalog.Vendor, match.Catalog.CellularDiagnosticParams()
	}
	return "", append([]string(nil), genericCellularFallback...)
}

// MatchCellularDiagnosticsForDevice is the model-aware counterpart to the
// compatibility method above. It returns whether the paths came from a
// qualified exact profile so APIs can surface that provenance to operators.
func (r *Registry) MatchCellularDiagnosticsForDevice(manufacturer, productClass string) (vendor string, paths []string, qualified bool) {
	match := r.MatchProfile(manufacturer, productClass)
	if match.MatchedBy != "" {
		return match.Catalog.Vendor, match.Catalog.CellularDiagnosticParams(), match.Qualified
	}
	return "", append([]string(nil), genericCellularFallback...), false
}
