package alerting

import "testing"

func TestResolveUsesSpecificityPrecedence(t *testing.T) {
	policies := []Policy{
		{ID: "fleet", Scope: ScopeFleet, Enabled: true},
		{ID: "tenant", Scope: ScopeTenant, TenantID: "premium-1", Enabled: true},
		{ID: "group", Scope: ScopeGroup, GroupID: "priority-sites", Enabled: true},
		{ID: "device", Scope: ScopeDevice, DeviceID: "cpe-1", Enabled: true},
	}
	got, ok := Resolve(policies, Target{TenantID: "premium-1", GroupIDs: []string{"priority-sites"}, DeviceID: "cpe-1"})
	if !ok || got.ID != "device" {
		t.Fatalf("Resolve() = %q, %v; want device, true", got.ID, ok)
	}
	got, ok = Resolve(policies, Target{TenantID: "premium-1", GroupIDs: []string{"priority-sites"}, DeviceID: "cpe-2"})
	if !ok || got.ID != "group" {
		t.Fatalf("Resolve() = %q, %v; want group, true", got.ID, ok)
	}
	got, ok = Resolve(policies, Target{TenantID: "premium-1", DeviceID: "cpe-3"})
	if !ok || got.ID != "tenant" {
		t.Fatalf("Resolve() = %q, %v; want tenant, true", got.ID, ok)
	}
}

func TestResolveSkipsDisabledAndDoesNotCrossTenant(t *testing.T) {
	policies := []Policy{
		{ID: "disabled", Scope: ScopeDevice, DeviceID: "cpe-1", Enabled: false},
		{ID: "other", Scope: ScopeTenant, TenantID: "tenant-2", Enabled: true},
		{ID: "fleet", Scope: ScopeFleet, Enabled: true},
	}
	got, ok := Resolve(policies, Target{TenantID: "tenant-1", DeviceID: "cpe-1"})
	if !ok || got.ID != "fleet" {
		t.Fatalf("Resolve() = %q, %v; want fleet, true", got.ID, ok)
	}
}

func TestClassifyUsesExactWildcardAndOfflineRules(t *testing.T) {
	p := Policy{FaultPriorities: map[string]Priority{"9002": P1, "9*": P2, "offline": P1}}
	if got := Classify(p, "9002", false); got != P1 {
		t.Fatalf("exact = %s", got)
	}
	if got := Classify(p, "9010", false); got != P2 {
		t.Fatalf("wildcard = %s", got)
	}
	if got := Classify(p, "anything", true); got != P1 {
		t.Fatalf("offline = %s", got)
	}
}
