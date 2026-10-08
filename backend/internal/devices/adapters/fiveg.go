package adapters

import (
	"acs/internal/datamodel"
	"acs/internal/parameters"
	"sort"
	"strconv"
	"strings"
	"time"
)

type FiveGMetric struct {
	Path     string     `json:"path"`
	Category string     `json:"category"`
	Standard bool       `json:"standard"`
	Raw      string     `json:"raw,omitempty"`
	Value    any        `json:"value"`
	Unit     string     `json:"unit,omitempty"`
	Source   string     `json:"source,omitempty"`
	AsOf     *time.Time `json:"as_of,omitempty"`
	State    string     `json:"state"`
}

// ProjectFiveG preserves all optional session/slice/QoS rows, rather than
// assuming PDU, SIM, access point and interface share the same instance number.
func ProjectFiveG(dictionary *datamodel.Dictionary, names map[string]bool, cache map[string]parameters.CachedValue, now time.Time, maxAge time.Duration) []FiveGMetric {
	out := []FiveGMetric{}
	paths := map[string]bool{}
	for path := range names {
		paths[path] = true
	}
	for path := range cache {
		if _, ok := names[datamodel.NormalizePath(path)]; ok {
			paths[path] = true
		}
	}
	for path := range paths {
		if strings.HasSuffix(path, ".") || strings.ContainsAny(path, "()!") {
			continue
		}
		lower := strings.ToLower(path)
		if !strings.HasPrefix(path, "Device.Cellular.") && !strings.HasPrefix(path, "Device.SessionManagement.") && !strings.HasPrefix(path, "Device.WWC.") && !strings.Contains(lower, "x_") {
			continue
		}
		if strings.Contains(lower, "x_") && !strings.Contains(lower, "cellular") && !strings.Contains(lower, "5g") && !strings.Contains(lower, "mobile") {
			continue
		}
		category := "cellular"
		if strings.Contains(path, "SessionManagement.") {
			category = "session"
		}
		if strings.Contains(path, "NetworkSlice.") || strings.Contains(path, "NSSAI") {
			category = "slice"
		}
		if strings.Contains(path, "QoS") {
			category = "qos"
		}
		if strings.Contains(path, "Registration") {
			category = "registration"
		}
		entry, standard := dictionary.Lookup(path)
		metric := FiveGMetric{Path: path, Category: category, Standard: standard, Unit: entry.Unit, State: "not_read"}
		if value, ok := cache[path]; ok {
			metric.Raw = value.Value
			metric.Source = value.Source
			stamp := value.UpdatedAt
			metric.AsOf = &stamp
			metric.Value = value.Value
			metric.State = "fresh"
			if now.Sub(stamp) > maxAge {
				metric.State = "stale"
			}
			if standard && (entry.Type == "int" || entry.Type == "long" || entry.Type == "unsignedInt" || entry.Type == "unsignedLong") {
				// Preserve counters exceeding the exact JSON/JavaScript integer range as strings.
				if integer, err := strconv.ParseInt(value.Value, 10, 64); err == nil && integer >= -9007199254740991 && integer <= 9007199254740991 {
					metric.Value = integer
				}
			}
			leaf := lastPathSegment(path)
			if standard && (leaf == "RSRP" || leaf == "RSRQ") && value.Value == "2147483647" {
				metric.Value = nil
				metric.State = "unavailable"
			}
			if value.Value == "Unknown" || value.Value == "" {
				metric.Value = nil
				metric.State = "unavailable"
			}
		}
		out = append(out, metric)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
