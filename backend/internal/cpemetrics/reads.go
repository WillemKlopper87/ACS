package cpemetrics

import (
	"math"
	"strings"

	"acs/internal/cwmp"
	"acs/internal/devices"
	"acs/internal/devices/adapters"
)

// Read is one parameter to fetch for a signal sample.
type Read struct {
	Field adapters.CellularField
	Path  string
}

// readFields is the plan order; instance "1" is the only interface sampled.
var readFields = []adapters.CellularField{
	adapters.CellularRSRP, adapters.CellularRSRQ, adapters.CellularSNR,
	adapters.CellularRSSI, adapters.CellularBand, adapters.CellularRAT,
}

// PlanReads resolves, from the device's discovered parameter names, the paths
// to read for one sample: only fields the names support, one entry per
// distinct path, in the order RSRP, RSRQ, SNR, RSSI, Band, RAT. It returns nil
// when names is empty or the data model is TR-098 (no portable cellular
// object). The plan is read-only evidence and never authorises a write.
func PlanReads(root string, names map[string]bool) []Read {
	if len(names) == 0 || root == devices.DataModelRootIGD1 || strings.HasPrefix(root, "InternetGatewayDevice") {
		return nil
	}
	var plan []Read
	seen := make(map[string]bool)
	for _, field := range readFields {
		res, ok := adapters.ResolveCellularReadPath(root, field, "1", names)
		if !ok || seen[res.Path] {
			continue
		}
		seen[res.Path] = true
		plan = append(plan, Read{Field: field, Path: res.Path})
	}
	return plan
}

// Parse builds a Sample from a GetParameterValues reply. Sentinel readings
// (e.g. -32768) are stored as nil. The bool is true only when at least one
// metric or text field is present.
func Parse(reads []Read, values []cwmp.ParameterValueStruct) (Sample, bool) {
	byPath := make(map[string]string, len(values))
	for _, v := range values {
		byPath[v.Name] = v.Value
	}
	var s Sample
	ok := false
	for _, r := range reads {
		raw, present := byPath[r.Path]
		if !present {
			continue
		}
		val := adapters.NormalizeCellularValue(raw).Value
		switch r.Field {
		case adapters.CellularRSRP:
			s.RSRP = number(val)
		case adapters.CellularRSRQ:
			s.RSRQ = number(val)
		case adapters.CellularSNR:
			s.SNR = number(val)
		case adapters.CellularRSSI:
			s.RSSI = number(val)
		case adapters.CellularBand:
			s.Band = text(raw)
		case adapters.CellularRAT:
			s.RAT = text(raw)
		}
	}
	ok = s.RSRP != nil || s.RSRQ != nil || s.SNR != nil || s.RSSI != nil || s.Band != "" || s.RAT != ""
	return s, ok
}

// number accepts only finite floats: NaN and Inf parse as numbers but cannot
// be encoded to JSON. No range check is applied, as vendors use other units.
func number(v any) *float64 {
	if f, isNum := v.(float64); isNum && !math.IsNaN(f) && !math.IsInf(f, 0) {
		return &f
	}
	return nil
}

// text takes the trimmed raw string so a bare numeric Band or RAT ("78") is
// kept; "no value" sentinels yield "".
func text(raw string) string {
	s := strings.TrimSpace(raw)
	if adapters.IsCellularSentinel(s) {
		return ""
	}
	return s
}
