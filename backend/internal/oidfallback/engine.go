// Package oidfallback provides read-only canonical telemetry resolution.
// Discovered standard paths precede firmware-qualified vendor paths and OIDs.
package oidfallback

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Identity struct {
	Vendor      string `json:"vendor"`
	Model       string `json:"model"`
	Firmware    string `json:"firmware"`
	SysObjectID string `json:"sys_object_id"`
}
type Mapping struct {
	StandardPaths []string `json:"standard_paths"`
	VendorPaths   []string `json:"vendor_paths"`
	OIDs          []string `json:"oids"`
	Scale         float64  `json:"oid_scale,omitempty"`
	Unit          string   `json:"unit,omitempty"`
}
type Profile struct {
	ID          string             `json:"id"`
	Vendor      string             `json:"vendor"`
	Models      []string           `json:"models"`
	Firmwares   []string           `json:"firmwares"`
	SysObjectID string             `json:"sys_object_id"`
	Evidence    string             `json:"evidence"`
	Fields      map[string]Mapping `json:"fields"`
}
type Reading struct {
	Available bool   `json:"available"`
	Value     any    `json:"value,omitempty"`
	Raw       string `json:"raw,omitempty"`
	Unit      string `json:"unit,omitempty"`
	Source    string `json:"source,omitempty"`
	Path      string `json:"path,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
type Reader interface {
	Read(context.Context, string) (string, bool, error)
}

var oidPattern = regexp.MustCompile(`^\.?1\.3\.6\.1(?:\.[0-9]+)+$`)

func NormalizeOID(oid string) string { return strings.TrimPrefix(oid, ".") }
func includes(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func (p Profile) Validate(identity Identity) error {
	if p.ID == "" || p.Evidence == "" || p.Vendor == "" || !strings.EqualFold(p.Vendor, identity.Vendor) || !includes(p.Models, identity.Model) || !includes(p.Firmwares, identity.Firmware) || identity.Firmware == "" {
		return fmt.Errorf("vendor, exact model and firmware must match an evidence-backed profile")
	}
	if !oidPattern.MatchString(p.SysObjectID) || NormalizeOID(p.SysObjectID) != NormalizeOID(identity.SysObjectID) {
		return fmt.Errorf("device sysObjectID does not match the qualified profile")
	}
	if len(p.Fields) == 0 || len(p.Fields) > 128 {
		return fmt.Errorf("profile requires 1..128 fields")
	}
	for field, mapping := range p.Fields {
		if field == "" || len(mapping.OIDs) > 8 || len(mapping.StandardPaths)+len(mapping.VendorPaths) > 16 || math.IsNaN(mapping.Scale) || math.IsInf(mapping.Scale, 0) {
			return fmt.Errorf("invalid mapping limits")
		}
		for _, oid := range mapping.OIDs {
			if !oidPattern.MatchString(oid) {
				return fmt.Errorf("invalid numeric OID in %s", field)
			}
		}
	}
	return nil
}

// Resolve distinguishes absence from transport errors, keeps raw values and
// explicit units, and never uses SNMP SET or guesses a vendor enterprise OID.
func Resolve(ctx context.Context, p Profile, identity Identity, discovered map[string]bool, values map[string]string, reader Reader) (map[string]Reading, error) {
	if err := p.Validate(identity); err != nil {
		return nil, err
	}
	out := map[string]Reading{}
	fields := make([]string, 0, len(p.Fields))
	for field := range p.Fields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		mapping := p.Fields[field]
		reading := Reading{Reason: "no discovered parameter or supported OID"}
		found := false
		for _, source := range []struct {
			paths []string
			kind  string
		}{{mapping.StandardPaths, "standard_parameter"}, {mapping.VendorPaths, "qualified_vendor_parameter"}} {
			for _, path := range source.paths {
				if _, exists := discovered[path]; !exists {
					continue
				}
				if raw, exists := values[path]; exists {
					reading = normalize(raw, mapping, source.kind, path)
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found && reader != nil {
			for _, oid := range mapping.OIDs {
				if err := ctx.Err(); err != nil {
					return out, err
				}
				raw, present, err := reader.Read(ctx, oid)
				if err != nil {
					reading.Reason = "SNMP read failed"
					break
				}
				if present {
					reading = normalize(raw, mapping, "qualified_snmp_oid", NormalizeOID(oid))
					break
				}
			}
		}
		out[field] = reading
	}
	return out, nil
}
func normalize(raw string, m Mapping, source, path string) Reading {
	reading := Reading{Available: true, Raw: raw, Value: raw, Unit: m.Unit, Source: source, Path: path}
	if source == "qualified_snmp_oid" && m.Scale != 0 && m.Scale != 1 {
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.IsInf(n*m.Scale, 0) {
			reading.Available = false
			reading.Value = nil
			reading.Reason = "cannot apply configured numeric scale"
			return reading
		}
		reading.Value = n * m.Scale
	}
	return reading
}
