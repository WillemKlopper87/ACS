package common

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestParsePage(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    PageRequest
		wantErr bool
	}{
		{name: "defaults", want: PageRequest{Offset: 0, Limit: DefaultPageLimit}},
		{name: "explicit", query: "offset=25&limit=50", want: PageRequest{Offset: 25, Limit: 50}},
		{name: "negative offset", query: "offset=-1", wantErr: true},
		{name: "non numeric offset", query: "offset=nope", wantErr: true},
		{name: "zero limit", query: "limit=0", wantErr: true},
		{name: "oversized limit", query: "limit=1001", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParsePage(values, DefaultPageLimit, MaxPageLimit)
			if tt.wantErr {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Status != 400 {
					t.Fatalf("expected 400 APIError, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %+v want %+v", got, tt.want)
			}
		})
	}
}

func TestParsePageNormalizesConfiguration(t *testing.T) {
	got, err := ParsePage(url.Values{}, -1, 25)
	if err != nil {
		t.Fatal(err)
	}
	if got.Limit != 25 {
		t.Fatalf("default must be clamped to max: got %d", got.Limit)
	}
}

func TestParseFields(t *testing.T) {
	all, err := ParseFields("", "id", "name")
	if err != nil {
		t.Fatal(err)
	}
	if !all.Includes("anything") || all.List() != nil {
		t.Fatal("empty fields query must mean all supported fields")
	}

	got, err := ParseFields(" name, id,name ", "id", "name")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Includes("id") || !got.Includes("name") || got.Includes("href") {
		t.Fatalf("unexpected projection: %#v", got.List())
	}
	if want := []string{"id", "name"}; !reflect.DeepEqual(got.List(), want) {
		t.Fatalf("got %v want %v", got.List(), want)
	}

	for _, raw := range []string{"id,,name", "unknown"} {
		if _, err := ParseFields(raw, "id", "name"); err == nil {
			t.Fatalf("expected %q to fail", raw)
		}
	}
}

func TestBuildHref(t *testing.T) {
	got, err := BuildHref("https://acs.example.test/root/", "tmf-api/resourceInventory/v5/resource", "device/a")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://acs.example.test/root/tmf-api/resourceInventory/v5/resource/device%2Fa"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	invalid := [][3]string{
		{"", "resource", "id"},
		{"/relative", "resource", "id"},
		{"https://acs.example.test", "../resource", "id"},
		{"https://acs.example.test?debug=true", "resource", "id"},
		{"https://acs.example.test", "resource", ""},
	}
	for _, args := range invalid {
		if _, err := BuildHref(args[0], args[1], args[2]); err == nil {
			t.Fatalf("expected BuildHref(%q, %q, %q) to fail", args[0], args[1], args[2])
		}
	}
}

func TestReferences(t *testing.T) {
	if !(EntityRef{ID: "1", Href: "https://example.test/1"}).Valid() {
		t.Fatal("complete entity ref must be valid")
	}
	if (EntityRef{ID: "1"}).Valid() {
		t.Fatal("entity ref without href must be invalid")
	}
	if !(ExternalReference{ID: "svc-1", Owner: "BSS"}).Valid() {
		t.Fatal("complete external ref must be valid")
	}
	if (ExternalReference{ID: "svc-1"}).Valid() {
		t.Fatal("external ref without owner must be invalid")
	}
}

func TestCorrelation(t *testing.T) {
	id, err := ParseCorrelationID("  order-123  ")
	if err != nil || id != "order-123" {
		t.Fatalf("got %q, %v", id, err)
	}
	for _, raw := range []string{"", "bad\nvalue", strings.Repeat("x", MaxCorrelationIDLength+1)} {
		if _, err := ParseCorrelationID(raw); err == nil {
			t.Fatalf("expected correlation id %q to fail", raw)
		}
	}
	if generated := NewCorrelationID(); generated == "" {
		t.Fatal("generated correlation ID must not be empty")
	}

	want := Correlation{RequestID: "req-1", ExternalID: "order-1", CauseID: "event-1"}
	ctx := WithCorrelation(context.Background(), want)
	got, ok := CorrelationFromContext(ctx)
	if !ok || got != want {
		t.Fatalf("got %+v, %v want %+v", got, ok, want)
	}
	if _, ok := CorrelationFromContext(nil); ok {
		t.Fatal("nil context must not contain correlation")
	}
}

func TestAPIError(t *testing.T) {
	err := InvalidQuery("limit", "too large")
	if err.Status != 400 || err.Code != "INVALID_QUERY" || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("unexpected invalid query error: %+v", err)
	}

	nf := NotFound("resource")
	if nf.Status != 404 || nf.Code != "NOT_FOUND" || nf.Error() != "resource not found" {
		t.Fatalf("unexpected not found error: %+v", nf)
	}
}
