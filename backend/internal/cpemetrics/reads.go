package cpemetrics

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/devices/adapters"
)

// Read is one parameter to fetch for a signal sample. Rank orders the paths
// of one field by preference: 0 is the most preferred (the 5G NR object),
// 1 the next (LTE), and so on.
type Read struct {
	Field adapters.CellularField
	Path  string
	Rank  int
}

// readFields is the plan order; instance "1" is the only interface sampled.
var readFields = []adapters.CellularField{
	adapters.CellularRSRP, adapters.CellularRSRQ, adapters.CellularSNR,
	adapters.CellularRSSI, adapters.CellularBand, adapters.CellularRAT,
}

// PlanReads resolves, from the device's discovered parameter names, the paths
// to read for one sample: only fields the names support, every path of a
// field (a CPE attached to one radio technology reports the other object's
// leaves as "no value"), one entry per distinct path, ordered by field
// (RSRP, RSRQ, SNR, RSSI, Band, RAT) and then by rank. It returns nil when
// names is empty or the data model is TR-098 (no portable cellular object).
// The plan is read-only evidence and never authorises a write.
func PlanReads(root string, names map[string]bool) []Read {
	if len(names) == 0 || root == devices.DataModelRootIGD1 || strings.HasPrefix(root, "InternetGatewayDevice") {
		return nil
	}
	var plan []Read
	seen := make(map[string]bool)
	for _, field := range readFields {
		rank := 0
		for _, res := range adapters.ResolveCellularReadPaths(root, field, "1", names) {
			if seen[res.Path] {
				continue
			}
			seen[res.Path] = true
			plan = append(plan, Read{Field: field, Path: res.Path, Rank: rank})
			rank++
		}
	}
	return plan
}

// Parse builds a Sample from a GetParameterValues reply. Per field it takes
// the first real value by rank, so a sentinel on the preferred object (the
// 5G leaves while attached only to LTE, or the reverse) falls through to the
// next one. Band comes from the object that supplied RSRP when it has one.
// Sentinel readings (e.g. -32768) are stored as nil. The bool is true only
// when at least one metric or text field is present.
func Parse(reads []Read, values []cwmp.ParameterValueStruct) (Sample, bool) {
	byPath := make(map[string]string, len(values))
	for _, v := range values {
		byPath[v.Name] = v.Value
	}
	ordered := append([]Read(nil), reads...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Rank < ordered[j].Rank })

	var s Sample
	var rsrpPath string
	for _, r := range ordered {
		raw, present := byPath[r.Path]
		if !present {
			continue
		}
		switch r.Field {
		case adapters.CellularRSRP:
			if s.RSRP == nil {
				s.RSRP = number(adapters.NormalizeCellularValue(raw).Value)
				if s.RSRP != nil {
					rsrpPath = r.Path
				}
			}
		case adapters.CellularRSRQ:
			if s.RSRQ == nil {
				s.RSRQ = number(adapters.NormalizeCellularValue(raw).Value)
			}
		case adapters.CellularSNR:
			if s.SNR == nil {
				s.SNR = number(adapters.NormalizeCellularValue(raw).Value)
			}
		case adapters.CellularRSSI:
			if s.RSSI == nil {
				s.RSSI = number(adapters.NormalizeCellularValue(raw).Value)
			}
		case adapters.CellularRAT:
			if s.RAT == "" {
				s.RAT = text(raw)
			}
		}
	}
	// Band: the serving object's band when RSRP came from it, else the first
	// non-empty one by rank.
	if rsrpPath != "" {
		for _, r := range ordered {
			if r.Field == adapters.CellularBand && parentPath(r.Path) == parentPath(rsrpPath) {
				if raw, present := byPath[r.Path]; present {
					if s.Band = text(raw); s.Band != "" {
						break
					}
				}
			}
		}
	}
	if s.Band == "" {
		for _, r := range ordered {
			if raw, present := byPath[r.Path]; present && r.Field == adapters.CellularBand {
				if s.Band = text(raw); s.Band != "" {
					break
				}
			}
		}
	}
	ok := s.RSRP != nil || s.RSRQ != nil || s.SNR != nil || s.RSSI != nil || s.Band != "" || s.RAT != ""
	return s, ok
}

// parentPath removes the last segment of a parameter path.
func parentPath(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i]
	}
	return path
}

// number accepts only finite floats: NaN and Inf parse as numbers but cannot
// be encoded to JSON. No range check is applied, as vendors use other units.
func number(v any) *float64 {
	if f, isNum := v.(float64); isNum && !math.IsNaN(f) && !math.IsInf(f, 0) {
		return &f
	}
	return nil
}

// maxTextRunes bounds Band and RAT: a CPE controls these strings and each is
// stored at every sample and returned to the console.
const maxTextRunes = 32

// text takes the trimmed raw string so a bare numeric Band or RAT ("78") is
// kept; "no value" sentinels yield "". Control characters are removed and the
// result is cut to maxTextRunes on a rune boundary.
func text(raw string) string {
	s := strings.TrimSpace(raw)
	if adapters.IsCellularSentinel(s) {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > maxTextRunes {
		s = string([]rune(s)[:maxTextRunes])
	}
	return s
}
