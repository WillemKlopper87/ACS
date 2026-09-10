package usp

import "testing"

func TestFormatEndpointID(t *testing.T) {
	cases := []struct {
		authority, instance string
		want                EndpointID
	}{
		// The obuspa default shape, from its own documentation.
		{"os", "012345-0800270B57FF", "os::012345-0800270B57FF"},
		// Alphanumerics and -._ stay literal; everything else is encoded
		// with uppercase hex.
		{"os", "a-b.c_d", "os::a-b.c_d"},
		{"os", "has space", "os::has%20space"},
		{"os", "sl/ash", "os::sl%2Fash"},
		{"os", "co:lon", "os::co%3Alon"},
		{"self", "acs-controller", "self::acs-controller"},
	}
	for _, c := range cases {
		if got := FormatEndpointID(c.authority, c.instance); got != c.want {
			t.Errorf("FormatEndpointID(%q, %q) = %q, want %q", c.authority, c.instance, got, c.want)
		}
	}
}

func TestEndpointIDAuthorityAndInstance(t *testing.T) {
	e := EndpointID("os::012345-0800270B57FF")
	if got := e.Authority(); got != "os" {
		t.Errorf("Authority() = %q, want os", got)
	}
	if got := e.Instance(); got != "012345-0800270B57FF" {
		t.Errorf("Instance() = %q, want 012345-0800270B57FF", got)
	}
	// Percent-encoded instances decode.
	if got := EndpointID("os::has%20space").Instance(); got != "has space" {
		t.Errorf("Instance() = %q, want %q", got, "has space")
	}
	// Malformed: no separator.
	if got := EndpointID("nonsense").Authority(); got != "" {
		t.Errorf("Authority() of a malformed id = %q, want empty", got)
	}
	if got := EndpointID("nonsense").Instance(); got != "" {
		t.Errorf("Instance() of a malformed id = %q, want empty", got)
	}
}

// OUISerial is an OPTIMISATION, not an identity source. It must succeed
// only for the exact os:: shape and refuse everything else, so a caller
// cannot accidentally mint a device record from an opaque endpoint id.
func TestOUISerialOnlyForWellFormedOSEndpoints(t *testing.T) {
	ouiSerial, ok := EndpointID("os::012345-0800270B57FF").OUISerial()
	if !ok || ouiSerial != "012345-0800270B57FF" {
		t.Errorf("OUISerial() = (%q, %v), want (012345-0800270B57FF, true)", ouiSerial, ok)
	}

	// A serial containing a hyphen: split on the LAST hyphen, so the OUI
	// is the first component and the serial keeps its own hyphens.
	ouiSerial, ok = EndpointID("os::012345-ABC-123").OUISerial()
	if !ok || ouiSerial != "012345-ABC-123" {
		t.Errorf("OUISerial() = (%q, %v), want the whole instance back", ouiSerial, ok)
	}

	for _, bad := range []EndpointID{
		"proto::agent-id",      // obuspa CI configs use this
		"self::acs-controller", // controller's own form
		"user::somebody",       // another valid authority scheme
		"os::nohyphen",         // no OUI/serial split
		"os::",                 // empty instance
		"os::-trailing",        // empty OUI component
		"os::trailing-",        // empty serial component
		"os::aa-bb-",           // multi-hyphen with empty serial: first-hyphen split would wrongly accept
		"os::-aa-bb",           // multi-hyphen with empty OUI: last-hyphen split would wrongly accept
		"nonsense",             // no separator at all
		"",                     // empty
	} {
		if got, ok := bad.OUISerial(); ok {
			t.Errorf("OUISerial() on %q returned (%q, true); an identity must not be derivable from it", bad, got)
		}
	}
}

func TestPercentRoundTrip(t *testing.T) {
	for _, s := range []string{"012345-0800270B57FF", "has space", "sl/ash", "a-b.c_d", "%", "100%sure"} {
		enc := PercentEncodeUSP(s)
		dec, err := PercentDecodeUSP(enc)
		if err != nil {
			t.Errorf("PercentDecodeUSP(%q) from %q: %v", enc, s, err)
			continue
		}
		if dec != s {
			t.Errorf("round trip of %q gave %q (encoded %q)", s, dec, enc)
		}
	}
	if _, err := PercentDecodeUSP("bad%zz"); err == nil {
		t.Error("PercentDecodeUSP(\"bad%zz\") returned nil error, want a decode error")
	}
}
