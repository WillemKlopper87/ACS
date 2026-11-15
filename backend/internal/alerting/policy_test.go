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

// Two customers can each have an account "5001". A tenant policy tied to
// one of them must not apply to the other's.
func TestResolveKeepsSameNumberedAccountsApart(t *testing.T) {
	policies := []Policy{
		{ID: "isp-a", Scope: ScopeTenant, TenantID: "5001", AccountKey: "key-a", Enabled: true},
		{ID: "fleet", Scope: ScopeFleet, Enabled: true},
	}
	if got, _ := Resolve(policies, Target{TenantID: "5001", AccountKey: "key-a"}); got.ID != "isp-a" {
		t.Fatalf("own account resolved to %q, want isp-a", got.ID)
	}
	if got, _ := Resolve(policies, Target{TenantID: "5001", AccountKey: "key-b"}); got.ID != "fleet" {
		t.Fatalf("another customer's account 5001 resolved to %q, want fleet", got.ID)
	}
	// A policy from before keys existed, or a target whose key is not
	// known, still matches on the ID.
	legacy := []Policy{{ID: "old", Scope: ScopeTenant, TenantID: "5001", Enabled: true}}
	if got, ok := Resolve(legacy, Target{TenantID: "5001", AccountKey: "key-b"}); !ok || got.ID != "old" {
		t.Fatalf("policy without a key resolved to %q, %v; want old", got.ID, ok)
	}
	if got, _ := Resolve(policies, Target{TenantID: "5001"}); got.ID != "isp-a" {
		t.Fatalf("target without a key resolved to %q, want isp-a", got.ID)
	}
}
