// Package subscriptions provides a generic, domain-agnostic desired-vs-actual
// reconciliation engine (Reconcile) plus a thin CRUD repository over the
// usp_subscriptions desired-state table (migration 0055).
//
// Reconcile itself knows nothing about USP or subscriptions: an Item's Key
// and Fingerprint are opaque strings supplied entirely by the caller, so the
// same diff engine can be reused by other reconciliation problems in this
// codebase (sub-project C's provisioning reconciliation) without this
// package importing anything USP-specific.
package subscriptions

// Item is one entry in a desired or actual state list handed to Reconcile.
// Key identifies *what* the item is (e.g. a subscription's ID); Fingerprint
// identifies *the desired/actual shape* of that item's content (e.g.
// NotifType+"|"+strings.Join(ReferenceList, ",")) so Reconcile can tell
// "same key, different content" apart from "same key, same content"
// without knowing what a subscription -- or any other domain concept -- is.
type Item struct {
	Key         string
	Fingerprint string
}

// Reconcile classifies the difference between a desired and an actual list
// of Items into what must be added and what must be removed. It is a pure
// function -- no I/O -- and makes no assumption about what Key or
// Fingerprint mean, which is what keeps it reusable outside USP
// subscriptions.
//
//   - A desired item whose Key is missing from actual is toAdd only.
//   - An actual item whose Key is missing from desired is toRemove only.
//   - A desired/actual pair sharing a Key but with different Fingerprints
//     has diverged: the actual item is stale (toRemove) and the desired
//     item is its replacement (toAdd) -- it appears in BOTH lists. This is
//     what Decisions' "delete-then-recreate" choice requires; Reconcile
//     does not sequence remove-before-add, that is the caller's job.
//   - A desired/actual pair sharing both Key and Fingerprint is already
//     converged and appears in neither list.
func Reconcile(desired, actual []Item) (toAdd, toRemove []Item) {
	actualByKey := make(map[string]Item, len(actual))
	for _, a := range actual {
		actualByKey[a.Key] = a
	}
	desiredByKey := make(map[string]Item, len(desired))
	for _, d := range desired {
		desiredByKey[d.Key] = d
	}

	for _, d := range desired {
		if a, ok := actualByKey[d.Key]; !ok || a.Fingerprint != d.Fingerprint {
			toAdd = append(toAdd, d)
		}
	}
	for _, a := range actual {
		if d, ok := desiredByKey[a.Key]; !ok || d.Fingerprint != a.Fingerprint {
			toRemove = append(toRemove, a)
		}
	}
	return toAdd, toRemove
}
