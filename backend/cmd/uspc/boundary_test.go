package main_test

import (
	"go/build"
	"strings"
	"testing"
)

// forbiddenPrefixes are the package trees cmd/uspc may not reach.
// internal/devices and internal/store are permitted: cmd/uspc's identity
// reconciler (identity.go) upserts devices and links usp_agents rows, so
// this service legitimately talks to the domain layer for identity.
// internal/jobs is permitted too, as of this plan's Task 4: dispatcher.go
// leases jobs, transitions their status, and drives internal/jobs'
// QueueListener -- the last of cmd/uspc's forbidden imports this plan
// named. No domain package is currently forbidden -- forbiddenPrefixes is
// empty, kept (not deleted) as a placeholder a future plan can populate
// if a new boundary is ever needed (e.g. keeping USP protocol negotiation
// free of billing logic), rather than discarding the path-boundary
// predicate and Imports/TestImports/XTestImports coverage this test
// already built.
var forbiddenPrefixes = []string{}

// forbiddenImport reports whether an import path is inside a forbidden
// tree, using the same path-boundary predicate as internal/usp's guard
// (B-1): an exact match or a match with a "/" boundary, so a sibling
// package that merely shares a prefix (e.g. acs/internal/storefront)
// does not trip it.
func forbiddenImport(imported string) bool {
	for _, root := range forbiddenPrefixes {
		if imported == root || strings.HasPrefix(imported, root+"/") {
			return true
		}
	}
	return false
}

// TestUSPCImportsNoDomainPackages enforces cmd/uspc's package-tree
// boundary against forbiddenPrefixes. It wires the USP protocol core
// (internal/usp, internal/usp/mtp) into a runnable service, reconciles
// agent identity (internal/devices, internal/store), and now (this plan's
// Task 3/Task 4) maps queued internal/jobs job types onto USP request
// messages and dispatches/leases/completes them -- every domain package
// cmd/uspc was ever going to need is now permitted, so forbiddenPrefixes
// is empty and this loop asserts nothing on its own. Real regression
// coverage lives in TestForbiddenPrefixesIsCurrentlyEmpty below: if a
// future plan repopulates forbiddenPrefixes, this loop starts asserting
// again without any further change here.
func TestUSPCImportsNoDomainPackages(t *testing.T) {
	pkg, err := build.Import("acs/cmd/uspc", "", 0)
	if err != nil {
		t.Fatalf("import acs/cmd/uspc: %v", err)
	}

	all := append([]string{}, pkg.Imports...)
	all = append(all, pkg.TestImports...)
	all = append(all, pkg.XTestImports...)

	for _, imported := range all {
		if forbiddenImport(imported) {
			t.Errorf("acs/cmd/uspc imports %s, which this plan forbids", imported)
		}
	}
}

// TestForbiddenPrefixesIsCurrentlyEmpty documents, as an explicit
// assertion rather than an empty unchecked loop, that forbiddenPrefixes
// currently forbids nothing: cmd/uspc has now been granted every domain
// package this plan named (internal/devices, internal/store,
// internal/jobs). It exists so a reader of go test's output sees a
// deliberate "nothing forbidden, checked" result instead of an assertion
// that silently has nothing to check.
func TestForbiddenPrefixesIsCurrentlyEmpty(t *testing.T) {
	if len(forbiddenPrefixes) != 0 {
		t.Fatalf("forbiddenPrefixes = %v, want empty -- update this test's expectations (and its doc comment) if a new boundary is deliberately being drawn", forbiddenPrefixes)
	}
}
