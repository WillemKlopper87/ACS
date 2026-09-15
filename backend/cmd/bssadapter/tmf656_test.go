package main

import (
	"acs/internal/bss"
	"reflect"
	"testing"
)

func TestTMFPageAndFieldSelection(t *testing.T) {
	o, end := tmfPage(7, 2, 3)
	if o != 2 || end != 5 {
		t.Fatalf("page = %d:%d, want 2:5", o, end)
	}
	got := tmfSelectMap(map[string]any{"id": "1", "status": "raised", "secret": "x"}, "id, status")
	if !reflect.DeepEqual(got, map[string]any{"id": "1", "status": "raised"}) {
		t.Fatalf("fields = %#v", got)
	}
}

func TestNormalizeTMF656IDsDeterministicAndDeduplicated(t *testing.T) {
	got := normalizeTMF656IDs([]string{" event-b ", "event-a", "event-b", "", "event-a"})
	want := []string{"event-a", "event-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTMF656ProblemResponseIncludesCorrelationAndImpact(t *testing.T) {
	p := &bss.ServiceProblemRecord{ID: "p1", AccountID: "acct-1", Status: "inProgress", RelatedEventIDs: []string{"e1"}, AffectedResourceIDs: []string{"r1"}, Impact: "degraded", Severity: "major"}
	got := tmf656ProblemResponse(p)
	for key, want := range map[string]any{"accountId": "acct-1", "impact": "degraded", "severity": "major"} {
		if got[key] != want {
			t.Fatalf("%s = %#v, want %#v", key, got[key], want)
		}
	}
	if !reflect.DeepEqual(got["relatedEventIds"], []string{"e1"}) {
		t.Fatalf("correlation missing: %#v", got["relatedEventIds"])
	}
}
