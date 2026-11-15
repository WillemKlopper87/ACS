package main

import "testing"

// A second region or project of the same name used to be a 500 with
// "internal error". Their names are unique: it is a conflict.
func TestDuplicateTenancyNameIsAConflict(t *testing.T) {
	e := newTestEnv(t)
	e.operator("root", "superadmin")
	for _, path := range []string{"/api/v1/regions", "/api/v1/projects"} {
		body := map[string]string{"name": "dup-" + path[8:]}
		if r := e.call("root", "POST", path, body); r.code != 201 {
			t.Fatalf("first POST %s → %d %s", path, r.code, r.body)
		}
		if r := e.call("root", "POST", path, body); r.code != 409 {
			t.Fatalf("second POST %s with the same name → %d %s, want 409", path, r.code, r.body)
		}
	}
}
