// Package datamodel indexes official, pinned BBF Device:2 dictionaries. A
// dictionary describes standard syntax, never a device's actual capabilities.
package datamodel

import (
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed models/*.xml
var models embed.FS

type Entry struct {
	Path        string      `json:"path"`
	Kind        string      `json:"kind"`
	Access      string      `json:"access"`
	Version     string      `json:"introduced_in,omitempty"`
	Type        string      `json:"type,omitempty"`
	Unit        string      `json:"unit,omitempty"`
	Constraints Constraints `json:"constraints,omitempty"`
}
type NumericRange struct {
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`
}
type SizeRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}
type Constraints struct {
	Enums  []string       `json:"enums,omitempty"`
	Ranges []NumericRange `json:"ranges,omitempty"`
	Sizes  []SizeRange    `json:"sizes,omitempty"`
	List   bool           `json:"list,omitempty"`
}
type Dictionary struct {
	Spec     string           `json:"spec"`
	Model    string           `json:"model"`
	Protocol string           `json:"protocol"`
	Version  string           `json:"version"`
	Entries  map[string]Entry `json:"-"`
}
type node struct {
	XMLName      xml.Name
	Name         string `xml:"name,attr"`
	Ref          string `xml:"ref,attr"`
	Base         string `xml:"base,attr"`
	Access       string `xml:"access,attr"`
	Version      string `xml:"version,attr"`
	Value        string `xml:"value,attr"`
	MinInclusive string `xml:"minInclusive,attr"`
	MaxInclusive string `xml:"maxInclusive,attr"`
	MinLength    int    `xml:"minLength,attr"`
	MaxLength    int    `xml:"maxLength,attr"`
	Children     []node `xml:",any"`
}
type document struct {
	Spec  string `xml:"spec,attr"`
	Types []node `xml:"dataType"`
	Model struct {
		Name    string `xml:"name,attr"`
		Objects []node `xml:"object"`
	} `xml:"model"`
}

var cache sync.Map
var digits = regexp.MustCompile(`^[1-9][0-9]*$`)

func NormalizePath(path string) string {
	parts := strings.Split(path, ".")
	for i, part := range parts {
		if digits.MatchString(part) {
			parts[i] = "{i}"
		}
	}
	return strings.Join(parts, ".")
}

func Load(protocol, version string) (*Dictionary, error) {
	protocol = strings.ToLower(protocol)
	if protocol != "cwmp" && protocol != "usp" {
		return nil, fmt.Errorf("protocol must be cwmp or usp")
	}
	if version == "" {
		version = "2.21"
	}
	if version != "2.20" && version != "2.21" {
		return nil, fmt.Errorf("bundled dictionary versions are 2.20 and 2.21")
	}
	key := protocol + "/" + version
	if d, ok := cache.Load(key); ok {
		return d.(*Dictionary), nil
	}
	raw, err := models.ReadFile("models/tr-181-" + strings.ReplaceAll(version, ".", "-") + "-0-" + protocol + "-full.xml")
	if err != nil {
		return nil, err
	}
	var doc document
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	types := map[string]node{}
	for _, n := range doc.Types {
		types[n.Name] = n
	}
	d := &Dictionary{Spec: doc.Spec, Model: doc.Model.Name, Protocol: protocol, Version: version, Entries: map[string]Entry{}}
	var visit func(node, string)
	visit = func(n node, prefix string) {
		switch n.XMLName.Local {
		case "object":
			path := n.Name
			if !strings.HasPrefix(path, "Device.") {
				path = prefix + path
			}
			if n.Name == "" {
				return
			}
			d.Entries[path] = Entry{Path: path, Kind: "object", Access: n.Access, Version: n.Version}
			for _, c := range n.Children {
				visit(c, path)
			}
		case "parameter", "command", "event":
			if n.Name == "" {
				return
			}
			path := prefix + n.Name
			d.Entries[path] = Entry{Path: path, Kind: n.XMLName.Local, Access: n.Access, Version: n.Version, Type: primitive(n, types, 0), Constraints: constraints(n, types, 0), Unit: unit(n, types, 0)}
		}
	}
	for _, n := range doc.Model.Objects {
		visit(n, "")
	}
	if len(d.Entries) < 1000 {
		return nil, fmt.Errorf("incomplete BBF dictionary: %d entries", len(d.Entries))
	}
	actual, _ := cache.LoadOrStore(key, d)
	return actual.(*Dictionary), nil
}

func primitive(n node, types map[string]node, depth int) string {
	if depth > 16 {
		return ""
	}
	switch n.XMLName.Local {
	case "string", "boolean", "int", "unsignedInt", "long", "unsignedLong", "dateTime", "base64", "hexBinary":
		return n.XMLName.Local
	case "dataType":
		if n.Ref != "" {
			return primitive(types[n.Ref], types, depth+1)
		}
		if n.Base != "" {
			return primitive(types[n.Base], types, depth+1)
		}
	}
	for _, c := range n.Children {
		if value := primitive(c, types, depth+1); value != "" {
			return value
		}
	}
	return ""
}

func (d *Dictionary) Lookup(path string) (Entry, bool) {
	e, ok := d.Entries[NormalizePath(path)]
	return e, ok
}

func constraints(n node, types map[string]node, depth int) Constraints {
	out := Constraints{}
	if depth > 16 {
		return out
	}
	if n.XMLName.Local == "dataType" {
		if n.Ref != "" {
			out = constraints(types[n.Ref], types, depth+1)
		}
		if n.Base != "" {
			out = constraints(types[n.Base], types, depth+1)
		}
	}
	for _, child := range n.Children {
		switch child.XMLName.Local {
		case "enumeration":
			out.Enums = append(out.Enums, child.Value)
		case "range":
			out.Ranges = append(out.Ranges, NumericRange{Min: child.MinInclusive, Max: child.MaxInclusive})
		case "size":
			out.Sizes = append(out.Sizes, SizeRange{Min: child.MinLength, Max: child.MaxLength})
		case "list":
			out.List = true
		case "description", "default", "pattern", "enumerationRef":
			continue
		default:
			c := constraints(child, types, depth+1)
			out.Enums = append(out.Enums, c.Enums...)
			out.Ranges = append(out.Ranges, c.Ranges...)
			out.Sizes = append(out.Sizes, c.Sizes...)
			out.List = out.List || c.List
		}
	}
	return out
}

func unit(n node, types map[string]node, depth int) string {
	if depth > 16 {
		return ""
	}
	if n.XMLName.Local == "units" {
		return n.Value
	}
	if n.XMLName.Local == "dataType" && n.Ref != "" {
		return unit(types[n.Ref], types, depth+1)
	}
	for _, child := range n.Children {
		if v := unit(child, types, depth+1); v != "" {
			return v
		}
	}
	return ""
}

// ValidateValue checks scalar lexical types. It is not a BBF certification or
// semantic conformance test: optional objects, device access rights, named
// type constraints and cross-parameter rules require live device evidence.
func (d *Dictionary) ValidateValue(path, value string) error {
	e, ok := d.Lookup(path)
	if !ok {
		return nil
	} // vendor extensions and newer revisions remain usable
	if e.Kind != "parameter" {
		return fmt.Errorf("%s is a %s, not a parameter", path, e.Kind)
	}
	values := []string{value}
	if e.Constraints.List {
		if value == "" {
			values = nil
		} else {
			values = strings.Split(value, ",")
		}
	}
	for _, item := range values {
		if err := validateScalar(e, item); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}
func validateScalar(e Entry, value string) error {
	var err error
	switch e.Type {
	case "boolean":
		if value != "0" && value != "1" && value != "true" && value != "false" {
			err = fmt.Errorf("expected boolean")
		}
	case "int":
		_, err = strconv.ParseInt(value, 10, 32)
	case "long":
		_, err = strconv.ParseInt(value, 10, 64)
	case "unsignedInt":
		_, err = strconv.ParseUint(value, 10, 32)
	case "unsignedLong":
		_, err = strconv.ParseUint(value, 10, 64)
	case "dateTime":
		_, err = time.Parse(time.RFC3339Nano, value)
	case "base64":
		_, err = base64.StdEncoding.DecodeString(value)
	case "hexBinary":
		_, err = hex.DecodeString(value)
	}
	if err != nil {
		return fmt.Errorf("invalid %s value", e.Type)
	}
	if len(e.Constraints.Enums) > 0 {
		valid := strings.HasPrefix(value, "X_")
		for _, allowed := range e.Constraints.Enums {
			valid = valid || value == allowed
		}
		if !valid {
			return fmt.Errorf("value is outside the standard enumeration")
		}
	}
	if len(e.Constraints.Ranges) > 0 {
		v, ok := new(big.Int).SetString(value, 10)
		if !ok {
			return fmt.Errorf("invalid integer")
		}
		valid := false
		for _, r := range e.Constraints.Ranges {
			within := true
			if r.Min != "" {
				if min, ok := new(big.Int).SetString(r.Min, 10); ok {
					within = within && v.Cmp(min) >= 0
				}
			}
			if r.Max != "" {
				if max, ok := new(big.Int).SetString(r.Max, 10); ok {
					within = within && v.Cmp(max) <= 0
				}
			}
			valid = valid || within
		}
		if !valid {
			return fmt.Errorf("value is outside the standard range")
		}
	}
	if len(e.Constraints.Sizes) > 0 {
		n := len(value)
		if e.Type == "hexBinary" {
			n /= 2
		}
		if e.Type == "base64" {
			decoded, _ := base64.StdEncoding.DecodeString(value)
			n = len(decoded)
		}
		valid := false
		for _, s := range e.Constraints.Sizes {
			valid = valid || n >= s.Min && (s.Max == 0 || n <= s.Max)
		}
		if !valid {
			return fmt.Errorf("value is outside the standard size limits")
		}
	}
	return nil
}
