package usp_test

import (
	"go/build"
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

// TestUSPImportsNoDomainPackages enforces design §4.1: internal/usp
// speaks protocol only. cmd/uspc is where protocol meets domain; if the
// codec could reach devices, jobs or store directly, that boundary would
// exist only in prose, and the first convenient import would erase it.
//
// This is a package-graph assertion rather than a grep, so it also
// catches a forbidden package reached transitively.
func TestUSPImportsNoDomainPackages(t *testing.T) {
	for _, pkgPath := range []string{"acs/internal/usp", "acs/internal/usp/uspproto"} {
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
