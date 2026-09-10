package main

import (
	"testing"

	"acs/internal/bss"
)

// An order that names no role targets the gateway. This is the contract
// decision that keeps every existing BSS caller working while replacing
// the "most recently updated" tiebreak with a deterministic target.
func TestRoleOrDefault(t *testing.T) {
	if got := roleOrDefault(""); got != bss.RoleGateway {
		t.Errorf("roleOrDefault(\"\") = %q, want %q", got, bss.RoleGateway)
	}
	if got := roleOrDefault(bss.RoleONT); got != bss.RoleONT {
		t.Errorf("roleOrDefault(ont) = %q, want ont", got)
	}
}
