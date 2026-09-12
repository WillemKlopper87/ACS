package usp_test

import (
	"go/build"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenPrefixes are the package trees internal/usp may not reach.
// Each is a path root: it matches itself and anything beneath it, with a
// real path boundary, so acs/internal/store forbids acs/internal/store/x
// but not a hypothetical sibling acs/internal/storefront.
var forbiddenPrefixes = []string{
	"acs/internal/devices",
	"acs/internal/jobs",
	"acs/internal/store",
	"acs/internal/sessions",
	"acs/internal/cwmp",
	"acs/cmd",
}

// uspModuleRoot is the import path of internal/usp itself; every package
// this guard walks must live under it.
const uspModuleRoot = "acs/internal/usp"

// forbiddenImport reports whether an import path is inside a forbidden
// tree. Factored out so the boundary behaviour can be tested against
// package names that need not exist.
func forbiddenImport(imported string) bool {
	for _, root := range forbiddenPrefixes {
		if imported == root || strings.HasPrefix(imported, root+"/") {
			return true
		}
	}
	return false
}

// TestForbiddenImportBoundary pins the path-boundary semantics: a sibling
// package that merely shares a prefix must not trip the guard.
func TestForbiddenImportBoundary(t *testing.T) {
	cases := map[string]bool{
		"acs/internal/store":               true,
		"acs/internal/store/sub":           true,
		"acs/cmd/api":                      true,
		"acs/cmd":                          true,
		"acs/internal/storefront":          false, // shares a prefix, is not the package
		"acs/internal/cwmpv2":              false,
		"acs/internal/usp":                 false,
		"acs/internal/usp/uspproto":        false,
		"google.golang.org/protobuf/proto": false,
	}
	for imported, want := range cases {
		if got := forbiddenImport(imported); got != want {
			t.Errorf("forbiddenImport(%q) = %v, want %v", imported, got, want)
		}
	}
}

// uspPackages walks the internal/usp directory tree and returns the
// import path of every directory that contains at least one .go file.
// Walking the filesystem, rather than hardcoding the package list, means
// a future sub-package (internal/usp/mtp, internal/usp/dm, ...) is
// covered by this guard automatically instead of silently escaping it.
func uspPackages(t *testing.T) []string {
	t.Helper()
	root, err := build.Import(uspModuleRoot, "", build.FindOnly)
	if err != nil {
		t.Fatalf("locate %s: %v", uspModuleRoot, err)
	}

	var pkgs []string
	seen := map[string]bool{}
	err = filepath.WalkDir(root.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		dir := filepath.Dir(path)
		if seen[dir] {
			return nil
		}
		seen[dir] = true
		rel, err := filepath.Rel(root.Dir, dir)
		if err != nil {
			return err
		}
		importPath := uspModuleRoot
		if rel != "." {
			importPath = uspModuleRoot + "/" + filepath.ToSlash(rel)
		}
		pkgs = append(pkgs, importPath)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root.Dir, err)
	}
	return pkgs
}

// TestUSPImportsNoDomainPackages enforces design §4.1: internal/usp
// speaks protocol only. cmd/uspc is where protocol meets domain; if the
// codec could reach devices, jobs or store directly, that boundary would
// exist only in prose, and the first convenient import would erase it.
//
// This checks the DIRECT imports of every package under internal/usp
// (build.Import returns direct imports only, not the transitive
// closure). It still prevents a forbidden package from being reached
// transitively, because every first-party package a member of this tree
// may import is itself required (by TestUSPTreeIsClosedUnderFirstPartyImports
// below) to be a member of the tree, and therefore itself checked here.
// A forbidden import reached via two hops inside the tree still shows up
// as a direct import on the intermediate package.
func TestUSPImportsNoDomainPackages(t *testing.T) {
	pkgs := uspPackages(t)
	// An empty walk would make this whole test vacuous. Assert we found
	// at least the two packages known to exist today, so a broken walk
	// (wrong root, wrong glob) cannot silently pass.
	want := map[string]bool{
		"acs/internal/usp":          false,
		"acs/internal/usp/uspproto": false,
	}
	for _, p := range pkgs {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, found := range want {
		if !found {
			t.Fatalf("uspPackages() did not find expected package %s (found: %v) -- walk is likely broken", p, pkgs)
		}
	}

	for _, pkgPath := range pkgs {
		pkg, err := build.Import(pkgPath, "", 0)
		if err != nil {
			t.Fatalf("import %s: %v", pkgPath, err)
		}
		// Imports of the package itself, plus its test files, since a
		// test that reached into the domain would defeat the point too.
		all := append([]string{}, pkg.Imports...)
		all = append(all, pkg.TestImports...)
		all = append(all, pkg.XTestImports...)

		for _, imported := range all {
			if forbiddenImport(imported) {
				t.Errorf("%s imports %s, which design §4.1 forbids: internal/usp speaks protocol only, and cmd/uspc is where protocol meets domain",
					pkgPath, imported)
			}
		}
	}
}

// TestUSPTreeIsClosedUnderFirstPartyImports is what actually makes
// TestUSPImportsNoDomainPackages transitive rather than merely direct:
// every first-party import (prefix "acs/") of a package under
// internal/usp must itself be under internal/usp. If some package in the
// tree could import another first-party package OUTSIDE the tree, that
// outside package's own imports would go unchecked here, and a forbidden
// package could be reached through it without ever showing up as a
// direct import of anything under internal/usp.
func TestUSPTreeIsClosedUnderFirstPartyImports(t *testing.T) {
	pkgs := uspPackages(t)
	for _, pkgPath := range pkgs {
		pkg, err := build.Import(pkgPath, "", 0)
		if err != nil {
			t.Fatalf("import %s: %v", pkgPath, err)
		}
		all := append([]string{}, pkg.Imports...)
		all = append(all, pkg.TestImports...)
		all = append(all, pkg.XTestImports...)

		for _, imported := range all {
			if !strings.HasPrefix(imported, "acs/") {
				continue // third-party or stdlib: not this test's concern
			}
			if imported != uspModuleRoot && !strings.HasPrefix(imported, uspModuleRoot+"/") {
				t.Errorf("%s imports first-party package %s outside internal/usp -- this breaks the transitivity of the domain-import guard, since %s's own imports are not walked by this test",
					pkgPath, imported, imported)
			}
		}
	}
}
