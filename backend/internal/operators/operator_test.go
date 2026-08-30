package operators

import "testing"

// TestAtLeastRankOrdering is the RBAC rank gate every one of cmd/api's
// ~72 route() registrations relies on (route(method, pattern, minRole,
// fn)) — zero test coverage existed for it before this pass, despite it
// being the actual enforcement boundary between a readonly operator and
// a write endpoint.
func TestAtLeastRankOrdering(t *testing.T) {
	cases := []struct {
		role, min string
		want      bool
	}{
		{RoleReadOnly, RoleReadOnly, true},
		{RoleReadOnly, RoleNOC, false},
		{RoleReadOnly, RoleManager, false},
		{RoleReadOnly, RoleSuperAdmin, false},
		{RoleNOC, RoleReadOnly, true},
		{RoleNOC, RoleNOC, true},
		{RoleNOC, RoleManager, false},
		{RoleManager, RoleNOC, true},
		{RoleManager, RoleManager, true},
		{RoleManager, RoleSuperAdmin, false},
		{RoleSuperAdmin, RoleReadOnly, true},
		{RoleSuperAdmin, RoleSuperAdmin, true},
	}
	for _, c := range cases {
		if got := AtLeast(c.role, c.min); got != c.want {
			t.Errorf("AtLeast(%q, %q) = %v, want %v", c.role, c.min, got, c.want)
		}
	}
}

func TestAtLeastUnrecognizedRoleNeverSatisfiesAnything(t *testing.T) {
	if AtLeast("not-a-real-role", RoleReadOnly) {
		t.Error("an unrecognized role should never satisfy even the lowest minimum")
	}
	if AtLeast(RoleSuperAdmin, "not-a-real-role") {
		t.Error("an unrecognized minimum should never be satisfiable")
	}
}

func TestAtLeastDeprecatedAliasesStillRank(t *testing.T) {
	// RoleOperator/RoleAdmin are kept as aliases for RoleNOC/RoleSuperAdmin
	// (operator.go's doc comment) so any stray old reference still
	// compiles and ranks correctly.
	if RoleOperator != RoleNOC {
		t.Errorf("RoleOperator alias drifted from RoleNOC: %q != %q", RoleOperator, RoleNOC)
	}
	if RoleAdmin != RoleSuperAdmin {
		t.Errorf("RoleAdmin alias drifted from RoleSuperAdmin: %q != %q", RoleAdmin, RoleSuperAdmin)
	}
}

func TestValidRole(t *testing.T) {
	for _, r := range AllRoles {
		if !ValidRole(r) {
			t.Errorf("ValidRole(%q) = false, want true — every AllRoles entry must be valid", r)
		}
	}
	if ValidRole("not-a-real-role") {
		t.Error("ValidRole should reject unknown role strings")
	}
	if ValidRole("") {
		t.Error("ValidRole should reject the empty string")
	}
}

func TestAllRolesAscendingRank(t *testing.T) {
	for i := 1; i < len(AllRoles); i++ {
		if AtLeast(AllRoles[i-1], AllRoles[i]) {
			t.Errorf("AllRoles must be strictly ascending by rank: %q should not satisfy minimum %q", AllRoles[i-1], AllRoles[i])
		}
		if !AtLeast(AllRoles[i], AllRoles[i-1]) {
			t.Errorf("AllRoles must be strictly ascending by rank: %q should satisfy the lower minimum %q", AllRoles[i], AllRoles[i-1])
		}
	}
}
