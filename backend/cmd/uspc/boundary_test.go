package main_test

import (
	"go/build"
	"strings"
	"testing"
)

// forbiddenPrefixes are the package trees cmd/uspc may not reach: this
// plan wires transport to protocol only, never to the domain layer
// (design §4.1) -- cmd/uspc talks USP, not devices/jobs/store directly.
var forbiddenPrefixes = []string{
	"acs/internal/devices",
	"acs/internal/jobs",
	"acs/internal/store",
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
// service, never imports the domain packages this plan explicitly keeps
// out of scope.
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
			t.Errorf("acs/cmd/uspc imports %s, which this plan forbids: cmd/uspc wires protocol to transport, not to the domain layer", imported)
		}
	}
}
