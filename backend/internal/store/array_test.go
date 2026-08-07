package store

import "testing"

func TestStringArrayRoundTrip(t *testing.T) {
	cases := [][]string{
		{},
		{"0 BOOTSTRAP"},
		{"0 BOOTSTRAP", "1 BOOT"},
		{`quote"inside`, `back\slash`, "plain,comma"},
	}

	for _, c := range cases {
		a := StringArray(c)
		val, err := a.Value()
		if err != nil {
			t.Fatalf("Value() error for %v: %v", c, err)
		}

		var got StringArray
		if err := got.Scan(val); err != nil {
			t.Fatalf("Scan() error for %v: %v", c, err)
		}

		if len(got) != len(c) {
			t.Fatalf("round-trip %v -> %v -> %v, length mismatch", c, val, got)
		}
		for i := range c {
			if got[i] != c[i] {
				t.Errorf("round-trip %v -> %v -> %v, element %d mismatch: got %q want %q", c, val, got, i, got[i], c[i])
			}
		}
	}
}

func TestStringArrayScanNil(t *testing.T) {
	var a StringArray
	if err := a.Scan(nil); err != nil {
		t.Fatalf("Scan(nil) error: %v", err)
	}
	if a != nil {
		t.Errorf("Scan(nil) = %v, want nil", a)
	}
}

func TestParsePostgresArrayLiteral(t *testing.T) {
	got := parsePostgresArray(`{"0 BOOTSTRAP","1 BOOT"}`)
	want := StringArray{"0 BOOTSTRAP", "1 BOOT"}
	if len(got) != len(want) {
		t.Fatalf("parsePostgresArray() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parsePostgresArray()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
