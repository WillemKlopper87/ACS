package usp_test

import (
	"go/build"
	"strings"
	"testing"
)

// TestUSPImportsNoDomainPackages enforces design §4.1: internal/usp
// speaks protocol only. cmd/uspc is where protocol meets domain; if the
// codec could reach devices, jobs or store directly, that boundary would
// exist only in prose, and the first convenient import would erase it.
//
// This is a package-graph assertion rather than a grep, so it also
// catches a forbidden package reached transitively.
func TestUSPImportsNoDomainPackages(t *testing.T) {
	forbidden := []string{
		"acs/internal/devices",
		"acs/internal/jobs",
		"acs/internal/store",
		"acs/internal/sessions",
		"acs/internal/cwmp",
		"acs/cmd/",
	}

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
			for _, bad := range forbidden {
				if imported == strings.TrimSuffix(bad, "/") || strings.HasPrefix(imported, bad) {
					t.Errorf("%s imports %s, which design §4.1 forbids: internal/usp speaks protocol only, and cmd/uspc is where protocol meets domain",
						pkgPath, imported)
				}
			}
		}
	}
}
