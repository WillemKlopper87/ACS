// Package usp implements the TR-369/USP protocol core: the Record
// envelope, message encoding and decoding, Endpoint IDs and error
// mapping.
//
// It speaks protocol only. It deliberately imports nothing from
// internal/devices, internal/jobs or internal/store (design §4.1), so
// the codec stays independently testable and the wiring between protocol
// and domain lives in cmd/uspc rather than here. boundary_test.go
// enforces that.
package usp

import (
	"fmt"
	"strings"
)

// EndpointID is a USP endpoint address of the form
// "<authority-scheme>::<instance>", e.g. "os::012345-0800270B57FF".
//
// It is a ROUTING ADDRESS, not an identity. Device identity is
// OUI + ProductClass + SerialNumber and is established from a
// Notify.OnBoardRequest or a Get of Device.DeviceInfo -- never from an
// endpoint id alone, because an agent's id can be overridden by a
// database value or an environment variable, and the authority scheme
// may be opaque (proto::, self::, user::). See design §5.1 and §5.3.
type EndpointID string

// endpointSeparator divides the authority scheme from the instance.
const endpointSeparator = "::"

// FormatEndpointID builds an endpoint id, percent-encoding the instance.
func FormatEndpointID(authority, instance string) EndpointID {
	return EndpointID(authority + endpointSeparator + PercentEncodeUSP(instance))
}

// Authority returns the authority scheme, or "" if the id is malformed.
func (e EndpointID) Authority() string {
	authority, _, ok := e.split()
	if !ok {
		return ""
	}
	return authority
}

// Instance returns the percent-decoded instance, or "" if the id is
// malformed or its encoding is invalid.
func (e EndpointID) Instance() string {
	_, instance, ok := e.split()
	if !ok {
		return ""
	}
	decoded, err := PercentDecodeUSP(instance)
	if err != nil {
		return ""
	}
	return decoded
}

func (e EndpointID) split() (authority, instance string, ok bool) {
	authority, instance, found := strings.Cut(string(e), endpointSeparator)
	if !found || authority == "" {
		return "", "", false
	}
	return authority, instance, true
}

// OUISerial extracts an "<OUI>-<SerialNumber>" identity from an endpoint
// id, and reports whether the id had that exact shape.
//
// This is an OPTIMISATION for an agent already in the registry, not a
// way to establish identity. It succeeds only for the "os::" authority
// with an instance that splits into two non-empty parts on its last
// hyphen -- the form the Broadband Forum reference agent derives from
// ManufacturerOUI and SerialNumber. Every other shape returns false, so
// a caller cannot mint a device record from an opaque id and produce a
// duplicate fleet.
func (e EndpointID) OUISerial() (ouiSerial string, ok bool) {
	authority, _, valid := e.split()
	if !valid || authority != "os" {
		return "", false
	}
	instance := e.Instance()
	// Both components must be non-empty. A serial may itself contain
	// hyphens, so neither "first hyphen" nor "last hyphen" alone is the
	// right split: a leading hyphen means an empty OUI, a trailing one an
	// empty serial, and either must be refused.
	if !strings.Contains(instance, "-") ||
		strings.HasPrefix(instance, "-") ||
		strings.HasSuffix(instance, "-") {
		return "", false
	}
	return instance, true
}

// uspSafeChars are the characters obuspa leaves literal when percent-
// encoding an endpoint id component, alongside alphanumerics.
const uspSafeChars = "-._"

// PercentEncodeUSP percent-encodes a string for use as an endpoint id
// instance: alphanumerics and -._ stay literal, everything else becomes
// %XX with uppercase hex.
func PercentEncodeUSP(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUSPUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// PercentDecodeUSP reverses PercentEncodeUSP.
func PercentDecodeUSP(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", fmt.Errorf("truncated percent escape at offset %d in %q", i, s)
		}
		hi, err := unhex(s[i+1])
		if err != nil {
			return "", fmt.Errorf("invalid percent escape at offset %d in %q: %w", i, s, err)
		}
		lo, err := unhex(s[i+2])
		if err != nil {
			return "", fmt.Errorf("invalid percent escape at offset %d in %q: %w", i, s, err)
		}
		b.WriteByte(hi<<4 | lo)
		i += 2
	}
	return b.String(), nil
}

func isUSPUnreserved(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte(uspSafeChars, c) >= 0
}

func unhex(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, fmt.Errorf("%q is not a hex digit", c)
}
