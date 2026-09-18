package adapters

import (
	"acs/internal/devices"
	"regexp"
	"strconv"
	"strings"
)

// CellularField is the stable, read-only vocabulary used by northbound
// consumers. A field may have more than one path because TR-181 has evolved
// and CPEs commonly expose an optional value on a related object.
type CellularField string

// CellularReadResolution records the evidence used to resolve a canonical
// cellular read. It is intentionally read-only evidence: callers must not use
// this result to authorize a write.
type CellularReadResolution struct {
	Path       string
	Standard   bool
	Discovered bool
}

const (
	CellularStatus        CellularField = "status"
	CellularAccessPoint   CellularField = "access_point"
	CellularSIMStatus     CellularField = "sim_status"
	CellularIMSI          CellularField = "imsi"
	CellularICCID         CellularField = "iccid"
	CellularIMEI          CellularField = "imei"
	CellularOperator      CellularField = "operator"
	CellularCellID        CellularField = "cell_id"
	CellularRAT           CellularField = "rat"
	CellularBand          CellularField = "band"
	CellularChannel       CellularField = "channel"
	CellularRSSI          CellularField = "rssi"
	CellularRSRP          CellularField = "rsrp"
	CellularRSRQ          CellularField = "rsrq"
	CellularBytesSent     CellularField = "bytes_sent"
	CellularBytesReceived CellularField = "bytes_received"
)

// CellularPathCandidates returns standard Device:2 paths for one interface.
// Paths are read candidates only: this table must never be used to authorize
// a SetParameterValues operation. Vendor extensions are supplied by catalogs
// and live discovery instead.
func CellularPathCandidates(root string, field CellularField, instance string) []string {
	if instance == "" {
		instance = "1"
	}
	prefix := "Device.Cellular.Interface." + instance + "."
	if root == devices.DataModelRootIGD1 {
		return nil // TR-098 has no portable cellular object equivalent.
	}
	paths := map[CellularField][]string{
		CellularStatus:      {prefix + "Status"},
		CellularAccessPoint: {"Device.Cellular.AccessPoint." + instance + ".APN"},
		CellularSIMStatus:   {prefix + "USIM.1.Status"},
		CellularIMSI:        {prefix + "USIM.1.IMSI"},
		CellularICCID:       {prefix + "USIM.1.ICCID"},
		CellularIMEI:        {prefix + "IMEI"},
		// Operator, cell ID, band and channel are not portable TR-181
		// leaves. They require a discovered path or model-qualified pack.
		CellularOperator:      nil,
		CellularCellID:        nil,
		CellularRAT:           {prefix + "CurrentAccessTechnology"},
		CellularBand:          nil,
		CellularChannel:       nil,
		CellularRSSI:          {prefix + "RSSI"},
		CellularRSRP:          {prefix + "RSRP"},
		CellularRSRQ:          {prefix + "RSRQ"},
		CellularBytesSent:     {prefix + "Stats.BytesSent"},
		CellularBytesReceived: {prefix + "Stats.BytesReceived"},
	}
	return append([]string(nil), paths[field]...)
}

// ResolveCellularReadPath resolves one canonical cellular field from the
// portable TR-181 candidates and, when supplied, the device's discovered
// parameter names. Discovered names are accepted only when their leaf matches
// a conservative field-specific vocabulary; arbitrary paths are never
// guessed. The returned bool is false when the device has no evidence for the
// requested field.
func ResolveCellularReadPath(root string, field CellularField, instance string, discovered map[string]bool) (CellularReadResolution, bool) {
	for _, path := range CellularPathCandidates(root, field, instance) {
		// Discovery's bool is writability, not presence. Read-only telemetry
		// is valid evidence for a read and must not be skipped.
		if len(discovered) == 0 {
			return CellularReadResolution{Path: path, Standard: true}, true
		}
		if _, present := discovered[path]; present {
			return CellularReadResolution{Path: path, Standard: true}, true
		}
	}
	if len(discovered) == 0 {
		return CellularReadResolution{}, false
	}
	standard := make(map[string]struct{})
	for _, path := range CellularPathCandidates(root, field, instance) {
		standard[path] = struct{}{}
	}
	for path := range discovered {
		if _, isStandard := standard[path]; isStandard {
			continue
		}
		if cellularLeafMatches(field, lastPathSegment(path)) {
			return CellularReadResolution{Path: path, Discovered: true}, true
		}
	}
	return CellularReadResolution{}, false
}

// CellularStateFields is the subset of cellular fields that represent a
// discrete operational state rather than continuous telemetry. Signal levels
// and byte counters change on almost every poll and are deliberately
// excluded so a BSS/OSS event export stays a meaningful state-change stream
// instead of a copy of every poll interval.
var CellularStateFields = []CellularField{
	CellularStatus, CellularAccessPoint, CellularSIMStatus,
	CellularOperator, CellularCellID, CellularRAT,
}

// cellularInstancePattern extracts the interface/access-point instance
// number from a reported path so a standard candidate can be reconstructed
// for exact comparison; it defaults to instance "1" when absent.
var cellularInstancePattern = regexp.MustCompile(`\.(?:Interface|AccessPoint)\.(\d+)\.`)

// MatchCellularStatePath identifies whether a reported parameter path
// corresponds to one of CellularStateFields. It first checks the path
// against the exact standard Device:2 candidate for each field — this
// matters because two standard leaves share the same last segment (the
// top-level interface Status and the nested USIM Status), and a bare
// leaf-alias comparison alone would conflate them. Only when no standard
// candidate matches does it fall back to the conservative leaf-alias
// vocabulary used for vendor/discovered paths.
func MatchCellularStatePath(path string) (CellularField, bool) {
	instance := "1"
	if m := cellularInstancePattern.FindStringSubmatch(path); m != nil {
		instance = m[1]
	}
	for _, field := range CellularStateFields {
		for _, candidate := range CellularPathCandidates(devices.DataModelRootDevice2, field, instance) {
			if candidate == path {
				return field, true
			}
		}
	}
	leaf := lastPathSegment(path)
	for _, field := range CellularStateFields {
		if cellularLeafMatches(field, leaf) {
			return field, true
		}
	}
	return "", false
}

func cellularLeafMatches(field CellularField, leaf string) bool {
	compact := strings.ToUpper(strings.NewReplacer("_", "", "-", "", " ", "").Replace(leaf))
	aliases := map[CellularField][]string{
		CellularStatus:      {"STATUS", "REGISTRATIONSTATUS", "SERVICESTATUS"},
		CellularAccessPoint: {"APN", "ACCESSPOINT"},
		CellularSIMStatus:   {"SIMSTATUS", "USIMSTATUS"},
		CellularIMSI:        {"IMSI"}, CellularICCID: {"ICCID"}, CellularIMEI: {"IMEI"},
		CellularOperator: {"OPERATOR", "CARRIER", "NETWORKNAME", "PLMN"},
		CellularCellID:   {"CELLID", "CELLIDENTITY", "ECGI", "NRCELLID"},
		CellularRAT:      {"RAT", "RADIOACCESS", "CURRENTACCESSTECHNOLOGY", "NETWORKTYPE"},
		CellularBand:     {"BAND", "BANDNUMBER"}, CellularChannel: {"CHANNEL", "ARFCN"},
		CellularRSSI: {"RSSI", "SIGNALSTRENGTH"}, CellularRSRP: {"RSRP"}, CellularRSRQ: {"RSRQ"},
		CellularBytesSent: {"BYTESSENT"}, CellularBytesReceived: {"BYTESRECEIVED"},
	}
	for _, alias := range aliases[field] {
		if compact == alias {
			return true
		}
	}
	return false
}

// CellularValue is a normalized read result while retaining the CPE's raw
// representation. Numeric values are represented as float64 only when the
// conversion is lossless enough for telemetry; otherwise Value is a string.
type CellularValue struct {
	Value any
	Raw   any
	Unit  string
}

// NormalizeCellularValue trims textual values and converts numeric strings.
// It deliberately does not convert dBm or counters between units: the unit
// must come from the data model/profile and guessing it would corrupt data.
func NormalizeCellularValue(raw any) CellularValue {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return CellularValue{Value: n, Raw: raw}
		}
		return CellularValue{Value: s, Raw: raw}
	case []byte:
		return NormalizeCellularValue(string(v))
	default:
		return CellularValue{Value: raw, Raw: raw}
	}
}
