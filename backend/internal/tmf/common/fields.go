package common

import (
	"sort"
	"strings"
)

// Fields is a validated projection set for TMF list/get endpoints. An empty
// fields query means all adapter-supported fields.
type Fields struct {
	all   bool
	names map[string]struct{}
}

// ParseFields validates a comma-separated fields parameter. If allowed is
// non-empty, unknown names are rejected explicitly rather than ignored.
func ParseFields(raw string, allowed ...string) (Fields, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Fields{all: true}, nil
	}

	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		name = strings.TrimSpace(name)
		if name != "" {
			allowedSet[name] = struct{}{}
		}
	}

	fields := Fields{names: make(map[string]struct{})}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			return Fields{}, InvalidQuery("fields", "contains an empty field name")
		}
		if len(allowedSet) > 0 {
			if _, ok := allowedSet[name]; !ok {
				return Fields{}, InvalidQuery("fields", "unsupported field "+name)
			}
		}
		fields.names[name] = struct{}{}
	}
	return fields, nil
}

// Includes reports whether a projected field should be emitted.
func (f Fields) Includes(name string) bool {
	if f.all {
		return true
	}
	_, ok := f.names[name]
	return ok
}

// List returns the requested projection in deterministic order. nil means all
// fields, matching ParseFields' empty-query semantics.
func (f Fields) List() []string {
	if f.all {
		return nil
	}
	out := make([]string, 0, len(f.names))
	for name := range f.names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
