package store

import (
	"database/sql/driver"
	"fmt"
	"strings"
)

// StringArray adapts a Postgres text[] column to a Go []string for use
// through the generic database/sql interface. pgx's stdlib driver accepts
// a bare []string as a query argument and encodes it correctly, but on
// the way back out, Rows.Scan has no sql.Scanner on *[]string and hands
// back the raw Postgres array text representation (e.g. `{"0
// BOOTSTRAP","1 BOOT"}`) as a string — this type parses that back into a
// slice.
type StringArray []string

func (a StringArray) Value() (driver.Value, error) {
	if a == nil {
		return nil, nil
	}
	parts := make([]string, len(a))
	escaper := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	for i, s := range a {
		parts[i] = `"` + escaper.Replace(s) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

func (a *StringArray) Scan(src any) error {
	if src == nil {
		*a = nil
		return nil
	}
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("StringArray.Scan: unsupported source type %T", src)
	}
	*a = parsePostgresArray(s)
	return nil
}

// parsePostgresArray parses Postgres's text array literal format
// (`{elem1,elem2,"quoted, elem"}`), handling quoted elements and
// backslash escapes. It does not handle nested arrays — not needed for
// the flat text[] columns this platform uses.
func parsePostgresArray(s string) StringArray {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return StringArray{}
	}
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")

	out := StringArray{}
	var buf strings.Builder
	inQuotes := false
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			buf.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			inQuotes = !inQuotes
		case r == ',' && !inQuotes:
			out = append(out, buf.String())
			buf.Reset()
		default:
			buf.WriteRune(r)
		}
	}
	out = append(out, buf.String())
	return out
}
