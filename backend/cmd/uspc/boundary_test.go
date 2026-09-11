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
// domain layer for identity now. internal/jobs is now permitted too
// (usp-job-dispatch plan, Task 3): dispatch.go imports it for job.Job,
// the Type* constants, and the payload structs to map a queued job onto
// a USP request message -- but never for a Repository method call
// (leasing, status transitions), which stays a later task's concern.
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
// agent identity (internal/devices, internal/store), and now (Task 3 of
// the usp-job-dispatch plan) maps queued internal/jobs job types onto
// USP request messages -- forbiddenPrefixes is currently empty, kept as
// a guard for whatever the next boundary turns out to be, rather than
// removing the mechanism along with the last entry.
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
