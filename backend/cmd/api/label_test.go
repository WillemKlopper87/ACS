package main

import (
	"strings"
	"testing"
)

// A device's identity (001349+NR5103+JOBDEMO00001) is an OUI, product class
// and serial glued together — unique, stable, and impossible for a human to
// remember or say out loud. The label is the name an operator actually uses
// for it: "Mrs Dlamini, 14 Oak Ave" or "Sandton POP rack 4, unit 2".
func TestNormalizeDeviceLabel(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "plain label", in: "Sandton POP rack 4", want: "Sandton POP rack 4"},
		{name: "surrounding whitespace is trimmed", in: "  Mrs Dlamini, 14 Oak Ave  ", want: "Mrs Dlamini, 14 Oak Ave"},
		{name: "empty clears the label", in: "", want: ""},
		{name: "whitespace only clears the label", in: "   ", want: ""},
		{
			name: "NUL bytes are refused",
			// A NUL byte cannot be stored in a Postgres text column at all,
			// so it has to be rejected before it reaches the driver.
			in:      "rack 4\x00unit 2",
			wantErr: "label cannot contain control characters",
		},
		{
			name: "newlines are refused",
			// The label is rendered on a single line everywhere it appears.
			in:      "rack 4\nunit 2",
			wantErr: "label cannot contain control characters",
		},
		{
			name:    "over-long label is refused",
			in:      strings.Repeat("a", maxDeviceLabelRunes+1),
			wantErr: "label cannot be longer than 120 characters",
		},
		{
			name: "a label exactly at the cap is accepted",
			in:   strings.Repeat("a", maxDeviceLabelRunes),
			want: strings.Repeat("a", maxDeviceLabelRunes),
		},
		{
			name: "multi-byte label is measured in characters, not bytes",
			// 20 three-byte runes: comfortably inside the rune cap, but over
			// it if someone measured len() on the raw bytes instead.
			in:   "設備設備設備設備設備設備設備設備設備設備",
			want: "設備設備設備設備設備設備設備設備設備設備",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeDeviceLabel(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got nil", tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Fatalf("expected error %q, got %q", tc.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("expected %q, got %q", tc.want, got)
			}
		})
	}
}
