// Package alerting contains the durable CPE incident and escalation rules.
package alerting

import "time"

type Priority string

const (
	P1 Priority = "P1"
	P2 Priority = "P2"
	P3 Priority = "P3"
	P4 Priority = "P4"
)

type Scope string

const (
	ScopeFleet  Scope = "fleet"
	ScopeTenant Scope = "tenant"
	ScopeGroup  Scope = "group"
	ScopeDevice Scope = "device"
)

type EscalationStep struct {
	After          time.Duration `json:"after"`
	Destination    string        `json:"destination"`
	Recipient      string        `json:"recipient"`
	NotifyOnRepeat bool          `json:"notify_on_repeat"`
}

type Policy struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Scope           Scope               `json:"scope"`
	TenantID        string              `json:"tenant_id,omitempty"`
	GroupID         string              `json:"group_id,omitempty"`
	DeviceID        string              `json:"device_id,omitempty"`
	CustomerTier    string              `json:"customer_tier,omitempty"`
	Enabled         bool                `json:"enabled"`
	FaultPriorities map[string]Priority `json:"fault_priorities"`
	OfflineAfter    time.Duration       `json:"offline_after"`
	Steps           []EscalationStep    `json:"steps"`
}

type Target struct {
	TenantID     string
	GroupIDs     []string
	DeviceID     string
	CustomerTier string
}

// Resolve selects the most specific enabled policy. Ties at one scope are
// resolved by list order, which is deterministic because repository queries
// order by priority and creation time.
func Resolve(policies []Policy, target Target) (Policy, bool) {
	best := Policy{}
	bestRank := -1
	for _, p := range policies {
		if !p.Enabled || !matches(p, target) {
			continue
		}
		rank := scopeRank(p.Scope)
		if rank > bestRank {
			best, bestRank = p, rank
		}
	}
	return best, bestRank >= 0
}

func matches(p Policy, t Target) bool {
	switch p.Scope {
	case ScopeDevice:
		return p.DeviceID != "" && p.DeviceID == t.DeviceID
	case ScopeGroup:
		for _, id := range t.GroupIDs {
			if p.GroupID != "" && p.GroupID == id {
				return true
			}
		}
		return false
	case ScopeTenant:
		return p.TenantID != "" && p.TenantID == t.TenantID
	case ScopeFleet:
		return p.TenantID == "" && p.GroupID == "" && p.DeviceID == ""
	default:
		return false
	}
}

func scopeRank(s Scope) int {
	switch s {
	case ScopeDevice:
		return 4
	case ScopeGroup:
		return 3
	case ScopeTenant:
		return 2
	case ScopeFleet:
		return 1
	default:
		return -1
	}
}
