package subscriptions

import (
	"reflect"
	"sort"
	"testing"
)

// sortItems gives Reconcile's output a stable order for comparison — the
// function itself makes no ordering promise, only a membership one.
func sortItems(items []Item) []Item {
	out := make([]Item, len(items))
	copy(out, items)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	return out
}

func TestReconcileAddsMissingDesired(t *testing.T) {
	desired := []Item{{Key: "a", Fingerprint: "f1"}}
	actual := []Item(nil)

	toAdd, toRemove := Reconcile(desired, actual)

	if !reflect.DeepEqual(sortItems(toAdd), []Item{{Key: "a", Fingerprint: "f1"}}) {
		t.Errorf("toAdd = %v, want [{a f1}]", toAdd)
	}
	if len(toRemove) != 0 {
		t.Errorf("toRemove = %v, want empty", toRemove)
	}
}

func TestReconcileRemovesUndesiredActual(t *testing.T) {
	desired := []Item(nil)
	actual := []Item{{Key: "a", Fingerprint: "f1"}}

	toAdd, toRemove := Reconcile(desired, actual)

	if len(toAdd) != 0 {
		t.Errorf("toAdd = %v, want empty", toAdd)
	}
	if !reflect.DeepEqual(sortItems(toRemove), []Item{{Key: "a", Fingerprint: "f1"}}) {
		t.Errorf("toRemove = %v, want [{a f1}]", toRemove)
	}
}

func TestReconcileConvergedItemUntouched(t *testing.T) {
	desired := []Item{{Key: "a", Fingerprint: "f1"}}
	actual := []Item{{Key: "a", Fingerprint: "f1"}}

	toAdd, toRemove := Reconcile(desired, actual)

	if len(toAdd) != 0 {
		t.Errorf("toAdd = %v, want empty", toAdd)
	}
	if len(toRemove) != 0 {
		t.Errorf("toRemove = %v, want empty", toRemove)
	}
}

func TestReconcileFingerprintMismatchRecreates(t *testing.T) {
	desired := []Item{{Key: "a", Fingerprint: "f2"}}
	actual := []Item{{Key: "a", Fingerprint: "f1"}}

	toAdd, toRemove := Reconcile(desired, actual)

	if !reflect.DeepEqual(sortItems(toAdd), []Item{{Key: "a", Fingerprint: "f2"}}) {
		t.Errorf("toAdd = %v, want [{a f2}]", toAdd)
	}
	if !reflect.DeepEqual(sortItems(toRemove), []Item{{Key: "a", Fingerprint: "f1"}}) {
		t.Errorf("toRemove = %v, want [{a f1}]", toRemove)
	}
}

func TestReconcileBothEmpty(t *testing.T) {
	toAdd, toRemove := Reconcile(nil, nil)

	if len(toAdd) != 0 {
		t.Errorf("toAdd = %v, want empty", toAdd)
	}
	if len(toRemove) != 0 {
		t.Errorf("toRemove = %v, want empty", toRemove)
	}
}

// TestReconcileMixed exercises all four cases together, guarding against an
// implementation that only works when the lists contain a single item.
func TestReconcileMixed(t *testing.T) {
	desired := []Item{
		{Key: "converged", Fingerprint: "same"},
		{Key: "added", Fingerprint: "new"},
		{Key: "changed", Fingerprint: "v2"},
	}
	actual := []Item{
		{Key: "converged", Fingerprint: "same"},
		{Key: "removed", Fingerprint: "gone"},
		{Key: "changed", Fingerprint: "v1"},
	}

	toAdd, toRemove := Reconcile(desired, actual)

	wantAdd := []Item{{Key: "added", Fingerprint: "new"}, {Key: "changed", Fingerprint: "v2"}}
	wantRemove := []Item{{Key: "changed", Fingerprint: "v1"}, {Key: "removed", Fingerprint: "gone"}}
	if !reflect.DeepEqual(sortItems(toAdd), sortItems(wantAdd)) {
		t.Errorf("toAdd = %v, want %v", toAdd, wantAdd)
	}
	if !reflect.DeepEqual(sortItems(toRemove), sortItems(wantRemove)) {
		t.Errorf("toRemove = %v, want %v", toRemove, wantRemove)
	}
}
