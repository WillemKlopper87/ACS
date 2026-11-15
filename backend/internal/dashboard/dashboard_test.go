package dashboard

import "testing"

func TestDecodeLayoutNullReadsAsDefaults(t *testing.T) {
	got, err := decodeLayout([]byte("null"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(DefaultWidgets) || len(got) == 0 {
		t.Fatalf("stored null layout = %d widgets, want the %d defaults", len(got), len(DefaultWidgets))
	}
}

func TestDecodeLayoutKeepsAnEmptyList(t *testing.T) {
	got, err := decodeLayout([]byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("stored empty layout = %v, want an empty list (the operator hid every widget)", got)
	}
}
