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
	ID       string `json:"id"`
	Name     string `json:"name"`
	Scope    Scope  `json:"scope"`
	TenantID string `json:"tenant_id,omitempty"`
	// CustomerID is the customer that owns the account TenantID names.
	// Account IDs are unique per customer, not across the ACS.
	CustomerID string `json:"customer_id,omitempty"`
	// AccountKey is the account's registry key (bss_accounts.id). Empty
	// on a policy made before accounts had keys, or for an ID no account
	// has yet: such a policy matches every account of that ID.
	AccountKey      string              `json:"-"`
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
	AccountKey   string // registry key of the account TenantID names, when known
	GroupIDs     []string
	DeviceID     string
	CustomerTier string
}

// Classify applies an explicit policy mapping first, then uses conservative
// defaults for operational conditions. Callers can map vendor fault codes to
// the policy keys without embedding vendor knowledge in the worker.
func Classify(p Policy, faultCode string, offline bool) Priority {
	if offline {
		if v, ok := p.FaultPriorities["offline"]; ok {
			return v
		}
		return P2
	}
	if v, ok := p.FaultPriorities[faultCode]; ok {
		return v
	}
	for key, v := range p.FaultPriorities {
		if key != "" && len(key) > 1 && key[len(key)-1] == '*' && len(faultCode) >= len(key)-1 && faultCode[:len(key)-1] == key[:len(key)-1] {
			return v
		}
	}
	switch faultCode {
	case "sim_change_alert":
		return P2 // a SIM on a network the plan does not allow
	case "sim_change_notice":
		return P4 // a different SIM on an allowed network, awaiting approval
	}
	return P3
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
		if p.TenantID == "" || p.TenantID != t.TenantID {
			return false
		}
		// Two customers can have an account of the same ID: when both
		// sides know which account they mean, it has to be the same one.
		if p.AccountKey != "" && t.AccountKey != "" && p.AccountKey != t.AccountKey {
			return false
		}
		return p.CustomerTier == "" || p.CustomerTier == t.CustomerTier
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
