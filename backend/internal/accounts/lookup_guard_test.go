package accounts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// textLookup matches SQL that finds or joins rows by the text account ID.
var textLookup = regexp.MustCompile(`account_id\s*=\s*(\$\d|ANY\b|NULLIF\b|[a-z][a-z0-9_]*\.account_id)|ON CONFLICT \(account_id`)

// allowed lists the only places that may use the text ID to find rows,
// each with the reason it is safe. Anything else must resolve the ID to a
// key first (Resolve, Ensure): once two customers can use the same ID, a
// lookup by text could return another customer's row.
var allowed = []struct{ file, contains, why string }{
	{"internal/accounts/accounts.go", "", "the registry: this is where an ID becomes a key"},
	{"internal/bss/accounts.go", "bss_accounts", "registry writes (claim, assign)"},
	{"internal/bss/events.go", "e.account_id = ANY", "the row is already limited to one customer"},
	{"internal/bss/customer_devices.go", "m.account_id = ANY", "the device is already limited to one customer"},
	{"internal/alerting/repository.go", "account_id=$1 AND device_id=$2", "qualified by the device, which is mapped to one account"},
}

func TestNoLookupByTextAccountID(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/accounts/accountstest/") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			lines := strings.Split(string(src), "\n")
			for i, line := range lines {
				if !textLookup.MatchString(line) {
					continue
				}
				// The statement may span lines: look at it with its neighbours.
				context := strings.Join(lines[max(0, i-3):min(len(lines), i+2)], "\n")
				ok := false
				for _, a := range allowed {
					if a.file == rel && strings.Contains(context, a.contains) {
						ok = true
					}
				}
				if !ok {
					t.Errorf("%s:%d finds rows by the text account ID; resolve it to account_key first (accounts.Resolve / accounts.Ensure):\n\t%s",
						rel, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
