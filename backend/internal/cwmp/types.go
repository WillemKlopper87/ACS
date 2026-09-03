// Package cwmp implements the CWMP/SOAP wire format used by TR-069 (Inform,
// RPC requests/responses, and Fault handling) plus the ACS-side session
// state needed to drive a probe session against a real CPE.
package cwmp

import "strings"

// SOAP/CWMP namespaces used on outbound envelopes. Inbound envelopes are
// parsed by local element name only (see envelope.go), so CPEs using
// different prefixes (soap-env vs soapenv vs SOAP-ENV) still parse
// correctly — only these constants matter for what we emit.
const (
	NSSoapEnvelope = "http://schemas.xmlsoap.org/soap/envelope/"
	NSSoapEncoding = "http://schemas.xmlsoap.org/soap/encoding/"
	NSXSD          = "http://www.w3.org/2001/XMLSchema"
	NSXSI          = "http://www.w3.org/2001/XMLSchema-instance"
	NSCWMP         = "urn:dslforum-org:cwmp-1-0"
)

// DeviceID is the CWMP DeviceIdStruct sent in every Inform.
type DeviceID struct {
	Manufacturer string `xml:"Manufacturer"`
	OUI          string `xml:"OUI"`
	ProductClass string `xml:"ProductClass"`
	SerialNumber string `xml:"SerialNumber"`
}

// NaturalKey is the device identity used across the platform: OUI +
// SerialNumber, falling back to including ProductClass when OUI+Serial
// alone is ambiguous for a vendor (v3 design doc §6.1).
//
// Each component is escaped before joining (audit M-5) so two different
// claimed identities can never collide onto the same key: without this,
// {OUI:"A", ProductClass:"", SerialNumber:"X+1"} and {OUI:"A",
// ProductClass:"X", SerialNumber:"1"} both joined to "A+X+1", letting a
// forged Inform alias an existing device's row. Fields with no literal
// "+" or "\" — the overwhelming majority of real OUI/ProductClass/Serial
// values — produce the exact same key as before this fix.
func (d DeviceID) NaturalKey() string {
	esc := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		return strings.ReplaceAll(s, `+`, `\+`)
	}
	if d.ProductClass != "" {
		return esc(d.OUI) + "+" + esc(d.ProductClass) + "+" + esc(d.SerialNumber)
	}
	return esc(d.OUI) + "+" + esc(d.SerialNumber)
}

// EventStruct is one entry in Inform's Event list.
type EventStruct struct {
	EventCode  string `xml:"EventCode"`
	CommandKey string `xml:"CommandKey"`
}

// ParameterValueStruct is one entry in a GetParameterValuesResponse or a
// SetParameterValues request's ParameterList.
type ParameterValueStruct struct {
	Name  string `xml:"Name"`
	Value string `xml:"Value"`
}

// ParameterInfoStruct is one entry in a GetParameterNamesResponse.
type ParameterInfoStruct struct {
	Name     string `xml:"Name"`
	Writable string `xml:"Writable"`
}
