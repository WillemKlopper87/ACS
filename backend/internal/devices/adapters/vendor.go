package adapters

import "strings"

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

func lastPathSegment(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

// genericCellularFallback is the standard TR-181 Cellular path set used
// when a device's manufacturer doesn't match any known vendor catalog —
// the same fallback a GenieACS provision script falls back to for an
// unrecognized vendor rather than giving up.
var genericCellularFallback = []string{
	"Device.Cellular.Interface.1.Stats.RSRP",
	"Device.Cellular.Interface.1.Stats.RSRQ",
	"Device.Cellular.Interface.1.Stats.SNR",
}

// Registry holds every vendor's parsed catalog, loaded once at startup.
// Read-only after construction, safe for concurrent use.
type Registry struct {
	catalogs map[string]Catalog // keyed by lowercased vendor name
}

func NewRegistry() *Registry {
	return &Registry{catalogs: LoadCatalogs()}
}

// MatchCellularDiagnostics returns the cellular/RF diagnostic parameter
// paths to poll for a device, matched by manufacturer name (case-
// insensitive substring — e.g. a reported manufacturer string of "ZYXEL
// Communications Corp." matches the "zyxel" catalog, mirroring the
// mfr.includes(...) matching a GenieACS provision script uses). Falls
// back to the generic TR-181 Cellular path set if nothing matches.
func (r *Registry) MatchCellularDiagnostics(manufacturer string) (vendor string, paths []string) {
	mfr := strings.ToLower(manufacturer)
	for key, cat := range r.catalogs {
		if key != "" && strings.Contains(mfr, key) {
			return cat.Vendor, cat.CellularDiagnosticParams()
		}
	}
	return "", append([]string(nil), genericCellularFallback...)
}
