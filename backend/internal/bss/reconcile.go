package bss

import (
	"strings"
	"time"
)

// Reconciliation field statuses.
const (
	ReconcileMatch        = "match"        // device's cached value equals what the BSS last ordered
	ReconcileDrift        = "drift"        // device's cached value differs from what the BSS last ordered
	ReconcileUnknown      = "unknown"      // no cached value at all -- the device has never reported this path (or its cache is stale/empty)
	ReconcileUnconfirmed  = "unconfirmed"  // the most recent order touching this path hasn't (yet) succeeded, so there is no confirmed BSS intent to compare against
	ReconcileUnverifiable = "unverifiable" // the path looks like a secret (password/passphrase) -- comparing a CPE's read-back to what was ordered isn't a safe or reliable check
)

// ReconciliationField is one canonical parameter's BSS-recorded intent
// (the most recent order that actually touched it) compared against the
// device's currently cached value.
type ReconciliationField struct {
	Path            string    `json:"path"`
	IntendedValue   string    `json:"intended_value"`
	ActualValue     string    `json:"actual_value,omitempty"`
	ActualUpdatedAt string    `json:"actual_updated_at,omitempty"`
	Status          string    `json:"status"`
	ExternalOrderID string    `json:"external_order_id"`
	OrderedAt       time.Time `json:"ordered_at"`
}

// DeviceReconciliation is the result of comparing a device's BSS order
// history against its current parameter cache.
type DeviceReconciliation struct {
	DeviceID  string                `json:"device_id"`
	AccountID string                `json:"account_id"`
	CheckedAt time.Time             `json:"checked_at"`
	Fields    []ReconciliationField `json:"fields"`
}

// secretLeafMarkers flags a path as write-only/unverifiable. TR-069 CPEs
// routinely mask a stored credential on read (empty string, a fixed
// placeholder, or simply refuse GetParameterValues on it) -- treating a
// mismatch there as "drift" would be a false positive on essentially every
// device, not a real signal.
var secretLeafMarkers = []string{"password", "passphrase", "presharedkey", "secret"}

func isSecretPath(path string) bool {
	lower := strings.ToLower(path)
	for _, marker := range secretLeafMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ComputeReconciliation is the pure drift computation. orders must already
// be sorted most-recent-first (OrdersForDevice's own contract). confirmed
// is the set of command_keys whose underlying ACS job reached SUCCESS --
// the caller resolves this via ACSClient.GetJobStatus, since bssadapter
// never has direct access to the jobs table (design §5.1). current is the
// device's live parameter cache for exactly the paths these orders touch
// (ACSClient.GetParameters).
//
// For each path, only the single most recent order that touched it is
// considered -- not "the most recent confirmed order for this path". If
// the newest order for a path hasn't succeeded yet, that is itself the
// correct thing to report (ReconcileUnconfirmed): falling back to an
// older, already-confirmed value would claim the device matches an intent
// that has since been superseded, which is misleading even though that
// older value might still be technically true on the wire right now.
func ComputeReconciliation(deviceID string, orders []OrderRecord, confirmed map[string]bool, current map[string]CachedParameter, now time.Time) DeviceReconciliation {
	result := DeviceReconciliation{DeviceID: deviceID, CheckedAt: now}
	claimed := make(map[string]bool)
	for _, order := range orders {
		if result.AccountID == "" {
			result.AccountID = order.AccountID
		}
		for _, p := range order.Parameters {
			if claimed[p.Name] {
				continue
			}
			claimed[p.Name] = true

			field := ReconciliationField{
				Path: p.Name, IntendedValue: p.Value,
				ExternalOrderID: order.ExternalOrderID, OrderedAt: order.CreatedAt,
			}
			switch {
			case !confirmed[order.CommandKey]:
				field.Status = ReconcileUnconfirmed
			case isSecretPath(p.Name):
				field.Status = ReconcileUnverifiable
			default:
				cached, ok := current[p.Name]
				if !ok {
					field.Status = ReconcileUnknown
				} else {
					field.ActualValue = cached.Value
					field.ActualUpdatedAt = cached.UpdatedAt
					if cached.Value == p.Value {
						field.Status = ReconcileMatch
					} else {
						field.Status = ReconcileDrift
					}
				}
			}
			result.Fields = append(result.Fields, field)
		}
	}
	return result
}

// ReconciliationPaths returns every distinct parameter path across orders
// -- what the caller needs to fetch from ACSClient.GetParameters before
// calling ComputeReconciliation. Order is unspecified.
func ReconciliationPaths(orders []OrderRecord) []string {
	seen := make(map[string]bool)
	var out []string
	for _, order := range orders {
		for _, p := range order.Parameters {
			if !seen[p.Name] {
				seen[p.Name] = true
				out = append(out, p.Name)
			}
		}
	}
	return out
}

// ReconciliationCommandKeys returns every distinct command_key across
// orders -- what the caller needs to resolve (via ACSClient.GetJobStatus)
// into the `confirmed` set ComputeReconciliation expects.
func ReconciliationCommandKeys(orders []OrderRecord) []string {
	seen := make(map[string]bool)
	var out []string
	for _, order := range orders {
		if order.CommandKey != "" && !seen[order.CommandKey] {
			seen[order.CommandKey] = true
			out = append(out, order.CommandKey)
		}
	}
	return out
}
