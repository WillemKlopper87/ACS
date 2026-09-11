package main_test

import (
	"go/build"
	"strings"
	"testing"
)

// forbiddenPrefixes are the package trees cmd/uspc may not reach.
// internal/devices and internal/store are now permitted: Task 5 gave
// cmd/uspc an identity reconciler (identity.go) that upserts devices and
// links usp_agents rows, so this service legitimately talks to the
// domain layer for identity now. internal/jobs stays forbidden until a
// later plan dispatches jobs from here.
var forbiddenPrefixes = []string{
	"acs/internal/jobs",
}

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

// TestUSPCImportsNoDomainPackages enforces that cmd/uspc, which wires
// the USP protocol core (internal/usp, internal/usp/mtp) into a runnable
// service and reconciles agent identity (internal/devices,
// internal/store), never imports internal/jobs -- job dispatch from
// cmd/uspc is a later plan's concern.
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
			t.Errorf("acs/cmd/uspc imports %s, which this plan forbids: cmd/uspc does not dispatch jobs", imported)
		}
	}
}
