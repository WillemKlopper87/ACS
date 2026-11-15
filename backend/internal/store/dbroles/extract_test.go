package dbroles

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// backendRoot is the module root, two levels up from this package... three:
// internal/store/dbroles.
func backendRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// schemaTables reads the table names from the migrations.
func schemaTables(t *testing.T, root string) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "internal", "store", "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	re := regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?(?:public\.)?"?([a-z_0-9]+)"?`)
	out := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			out[m[1]] = true
		}
	}
	return out
}

// closureDirs returns the directories of every package of this module that
// a service links, the service's own included.
func closureDirs(t *testing.T, root, cmd string) []string {
	t.Helper()
	c := exec.Command("go", "list", "-deps", "./cmd/"+cmd)
	c.Dir = root
	out, err := c.Output()
	if err != nil {
		t.Fatalf("go list ./cmd/%s: %v", cmd, err)
	}
	var dirs []string
	for _, pkg := range strings.Fields(string(out)) {
		if rel, ok := strings.CutPrefix(pkg, "acs/"); ok {
			dirs = append(dirs, filepath.Join(root, rel))
		}
	}
	return dirs
}

func sourceFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	var out []string
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	return out
}

var (
	rawLit    = regexp.MustCompile("`([^`]*)`")
	strLit    = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	isSQL     = regexp.MustCompile(`\b(SELECT|INSERT|UPDATE|DELETE|FROM|JOIN)\b`)
	fromJoin  = regexp.MustCompile(`\b(?:FROM|JOIN)\s+(?:ONLY\s+)?([a-z_0-9]+)`)
	insertInt = regexp.MustCompile(`\bINSERT INTO\s+([a-z_0-9]+)`)
	updateTab = regexp.MustCompile(`\bUPDATE\s+(?:ONLY\s+)?([a-z_0-9]+)`)
	deleteTab = regexp.MustCompile(`\bDELETE FROM\s+(?:ONLY\s+)?([a-z_0-9]+)`)
	rowLock   = regexp.MustCompile(`FOR (UPDATE|NO KEY UPDATE|SHARE|KEY SHARE)`)
)

// extract reads the privileges a service's code needs from the SQL in its
// string literals. It works per package: if a service links a package, it
// is taken to need every table that package's SQL names. That errs on the
// side of granting (see Denied for the reviewed exceptions), never on the
// side of a query failing in production.
func extract(t *testing.T, root, cmd string, tables map[string]bool) map[string]map[string]bool {
	t.Helper()
	need := map[string]map[string]bool{}
	add := func(table string, privs ...string) {
		if !tables[table] {
			return
		}
		if need[table] == nil {
			need[table] = map[string]bool{}
		}
		for _, p := range privs {
			need[table][p] = true
		}
	}
	names := func(re *regexp.Regexp, s string) []string {
		var out []string
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
		return out
	}
	for _, dir := range closureDirs(t, root, cmd) {
		for _, f := range sourceFiles(t, dir) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src := string(b)
			var lits []string
			for _, m := range rawLit.FindAllStringSubmatch(src, -1) {
				lits = append(lits, m[1])
			}
			for _, m := range strLit.FindAllStringSubmatch(src, -1) {
				lits = append(lits, m[1])
			}
			for _, lit := range lits {
				if !isSQL.MatchString(lit) {
					continue
				}
				read := names(fromJoin, lit)
				for _, tb := range read {
					add(tb, "SELECT")
				}
				inserted := names(insertInt, lit)
				for _, tb := range inserted {
					add(tb, "INSERT")
				}
				// UPDATE and DELETE read the rows they change.
				for _, tb := range names(updateTab, lit) {
					add(tb, "UPDATE", "SELECT")
				}
				for _, tb := range names(deleteTab, lit) {
					add(tb, "DELETE", "SELECT")
				}
				if strings.Contains(lit, "ON CONFLICT") && strings.Contains(lit, "DO UPDATE") {
					for _, tb := range inserted {
						add(tb, "UPDATE", "SELECT")
					}
				}
				// A row lock needs UPDATE on the locked table.
				if rowLock.MatchString(lit) {
					for _, tb := range read {
						add(tb, "UPDATE")
					}
				}
				if strings.Contains(lit, "RETURNING") {
					for _, tb := range inserted {
						add(tb, "SELECT")
					}
				}
			}
		}
	}
	for table, privs := range Extra[cmd] {
		add(table, privs...)
	}
	for table := range Denied[cmd] {
		delete(need, table)
	}
	return need
}

var privOrder = []string{"SELECT", "INSERT", "UPDATE", "DELETE"}

func ordered(set map[string]bool) []string {
	var out []string
	for _, p := range privOrder {
		if set[p] {
			out = append(out, p)
		}
	}
	return out
}

// TestManifestCoversTheCode fails when a service's code uses a table, or
// a kind of access to one, that its role is not granted: without this the
// first sign would be "permission denied" in production. Run with
// ACS_UPDATE_DB_GRANTS=1 to rewrite manifest.go from the code, then review
// the diff: a new grant is a change in what a service can reach.
func TestManifestCoversTheCode(t *testing.T) {
	root := backendRoot(t)
	tables := schemaTables(t, root)
	want := map[string]map[string][]string{}
	for _, svc := range Services {
		want[svc.Cmd] = map[string][]string{}
		for table, privs := range extract(t, root, svc.Cmd, tables) {
			want[svc.Cmd][table] = ordered(privs)
		}
	}

	if os.Getenv("ACS_UPDATE_DB_GRANTS") == "1" {
		writeManifest(t, want)
		t.Log("manifest.go rewritten from the code")
		return
	}

	for _, svc := range Services {
		for table, privs := range want[svc.Cmd] {
			have := map[string]bool{}
			for _, p := range Manifest[svc.Cmd][table] {
				have[p] = true
			}
			for _, p := range privs {
				if !have[p] {
					t.Errorf("cmd/%s uses %s on %s, but role %s is not granted it: run this test with ACS_UPDATE_DB_GRANTS=1 and review the change to manifest.go",
						svc.Cmd, p, table, svc.Role)
				}
			}
		}
		// The other direction: a grant nothing in the code needs is a
		// privilege with no reason to exist.
		for table, privs := range Manifest[svc.Cmd] {
			need := map[string]bool{}
			for _, p := range want[svc.Cmd][table] {
				need[p] = true
			}
			for _, p := range privs {
				if !need[p] {
					t.Errorf("role %s is granted %s on %s, which cmd/%s no longer uses: regenerate manifest.go", svc.Role, p, table, svc.Cmd)
				}
			}
		}
	}
}

// TestDeniedTablesAreUnreachable keeps the reviewed exceptions honest: if
// a service starts calling what would need a denied table, the denial has
// to be reconsidered, not discovered in production.
func TestDeniedTablesAreUnreachable(t *testing.T) {
	root := backendRoot(t)
	for cmd, tables := range Denied {
		for table, patterns := range tables {
			for _, pattern := range patterns {
				re := regexp.MustCompile(pattern)
				for _, dir := range closureDirs(t, root, cmd) {
					// The package that defines the SQL may mention its own
					// constructors; what matters is who calls them.
					if strings.HasSuffix(dir, filepath.Join("internal", "bss")) || strings.HasSuffix(dir, filepath.Join("internal", "store", "dbroles")) {
						continue
					}
					for _, f := range sourceFiles(t, dir) {
						b, _ := os.ReadFile(f)
						if re.Match(b) {
							rel, _ := filepath.Rel(root, f)
							t.Errorf("cmd/%s is denied %s, but %s matches %q: the service may now need that table", cmd, table, rel, pattern)
						}
					}
				}
			}
		}
	}
}

func writeManifest(t *testing.T, m map[string]map[string][]string) {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("// Code generated by TestManifestCoversTheCode with ACS_UPDATE_DB_GRANTS=1. DO NOT EDIT.\n\n")
	b.WriteString("package dbroles\n\n")
	b.WriteString("// Manifest is the table privileges of each service's database role, keyed\n// by the service's directory under cmd/. Review every change to it: it is\n// what each service can read and write.\n")
	b.WriteString("var Manifest = map[string]map[string][]string{\n")
	for _, svc := range Services {
		fmt.Fprintf(&b, "\t%q: {\n", svc.Cmd)
		tables := make([]string, 0, len(m[svc.Cmd]))
		for table := range m[svc.Cmd] {
			tables = append(tables, table)
		}
		sort.Strings(tables)
		for _, table := range tables {
			quoted := make([]string, len(m[svc.Cmd][table]))
			for i, p := range m[svc.Cmd][table] {
				quoted[i] = fmt.Sprintf("%q", p)
			}
			fmt.Fprintf(&b, "\t\t%q: {%s},\n", table, strings.Join(quoted, ", "))
		}
		b.WriteString("\t},\n")
	}
	b.WriteString("}\n")
	src, err := format.Source(b.Bytes())
	if err != nil {
		t.Fatalf("format manifest: %v", err)
	}
	if err := os.WriteFile("manifest.go", src, 0o644); err != nil {
		t.Fatal(err)
	}
}

// dynamicReadOnly lists tables that a file with dynamic SQL names in a
// string of their own but only reads: no DELETE is needed.
var dynamicReadOnly = map[string]string{
	"wifi_ssid_samples": "internal/wifiinsights passes it to the counterDeltas SELECT",
}

var dynamicTable = regexp.MustCompile("(?:DELETE FROM|FROM|INTO|UPDATE)\\s+(?:%s|`\\s*\\+)")

// TestDynamicTableNamesAreGranted covers the SQL the extractor cannot
// read: statements whose table name comes from a variable (retention's
// rule list, the Wi-Fi mesh clean-up). In a file that builds SQL that
// way, every string that is exactly a table name is taken to be a table
// the statement can delete from, and each service linking the package
// must be granted that; Extra is where it is written down. A new rule in
// internal/retention therefore fails here until the grant is added.
func TestDynamicTableNamesAreGranted(t *testing.T) {
	root := backendRoot(t)
	tables := schemaTables(t, root)
	found := 0
	for _, svc := range Services {
		for _, dir := range closureDirs(t, root, svc.Cmd) {
			if strings.HasSuffix(dir, filepath.Join("internal", "store", "dbroles")) {
				continue
			}
			for _, f := range sourceFiles(t, dir) {
				b, _ := os.ReadFile(f)
				if !dynamicTable.Match(b) {
					continue
				}
				found++
				rel, _ := filepath.Rel(root, f)
				for _, m := range strLit.FindAllStringSubmatch(string(b), -1) {
					table := m[1]
					if !tables[table] {
						continue
					}
					have := map[string]bool{}
					for _, p := range Manifest[svc.Cmd][table] {
						have[p] = true
					}
					if _, readOnly := dynamicReadOnly[table]; readOnly && have["SELECT"] {
						continue
					}
					if !have["DELETE"] || !have["SELECT"] {
						t.Errorf("%s builds SQL with a table name from a variable and names %q, but role %s lacks SELECT/DELETE on it: add it to Extra[%q] in roles.go and regenerate manifest.go",
							rel, table, svc.Role, svc.Cmd)
					}
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("no dynamically built SQL found: the pattern no longer matches internal/retention, so this test proves nothing")
	}
}
