// Package onboarding records who a CPE belongs to, where it is installed
// and what service it delivers, and moves it from a sale (PLANNED, before
// the device has ever connected) to IN_SERVICE
// (docs/CONSOLE-UX-PROPOSAL-2026-10-30.md §4).
//
// Evaluate is the whole state machine. It is a pure function of the
// onboarding, its plan and the facts the CWMP gateway gathered on an
// Inform, so the gateway can re-evaluate on every Inform: a missed or
// dropped session never strands an onboarding, the next Inform catches up.
package onboarding

import (
	"fmt"
	"strings"

	"acs/internal/parameters"
)

const (
	StatePlanned     = "PLANNED"
	StateContacted   = "CONTACTED"
	StateSecured     = "SECURED"
	StateDiscovered  = "DISCOVERED"
	StateConfiguring = "CONFIGURING"
	StateInService   = "IN_SERVICE"
	StateOnHold      = "ON_HOLD"
	StateFailed      = "FAILED"
	StateCancelled   = "CANCELLED"
)

// Facts are what the gateway knows about the device on this Inform.
type Facts struct {
	Informed bool
	// Secured: the device authenticates with its own credential (an ACTIVE
	// per-device CWMP Digest, or mTLS), not the shared bootstrap one.
	Secured    bool
	Discovered bool
	// SIM identifiers from the parameter cache; empty when not read yet.
	ICCID string
	IMSI  string
	// Outcome of the onboarding's configuration jobs.
	ConfigPending, ConfigDone, ConfigFailed int
	ConfigError                             string
}

// Decision is the next state and what the gateway must do for it.
type Decision struct {
	State  string
	Reason string
	// ReadSIM: queue a read of the SIM identifiers the checks need.
	ReadSIM bool
	// Configure: queue the plan's configuration (state becomes CONFIGURING).
	Configure bool
}

// Evaluate advances an onboarding as far as the facts allow.
func Evaluate(o Onboarding, plan *ServicePlan, f Facts) Decision {
	switch o.State {
	case StateInService, StateCancelled, StateFailed, StateOnHold:
		// Terminal, or waiting for an operator (Retry).
		return Decision{State: o.State, Reason: o.StateReason}
	}
	state := o.State
	if state == StatePlanned {
		if !f.Informed {
			return Decision{State: state}
		}
		state = StateContacted
	}

	if state == StateContacted {
		if d, stop := checkSIM(o, plan, f); stop {
			return d
		}
		if !f.Secured {
			return Decision{State: state, Reason: "waiting for the device to authenticate with its own credential"}
		}
		state = StateSecured
	}

	if state == StateSecured {
		if plan != nil && plan.RequireDiscovery && !f.Discovered {
			return Decision{State: state, Reason: "waiting for parameter discovery"}
		}
		// Straight on: the next Inform may be hours away, so a discovered
		// device is configured in the same pass. DISCOVERED is only ever
		// stored by an onboarding created before this pass existed.
		state = StateDiscovered
	}

	if state == StateDiscovered {
		if plan == nil || !plan.HasConfiguration() {
			return Decision{State: StateInService}
		}
		return Decision{State: StateConfiguring, Configure: true}
	}

	if state == StateConfiguring {
		switch {
		case f.ConfigFailed > 0:
			return Decision{State: StateFailed, Reason: "configuration failed: " + f.ConfigError}
		case f.ConfigPending > 0:
			return Decision{State: state, Reason: fmt.Sprintf("%d configuration job(s) waiting for the device", f.ConfigPending)}
		default:
			return Decision{State: StateInService}
		}
	}
	return Decision{State: state}
}

// checkSIM holds a device whose SIM is not the one sold with it, or not on
// an allowed network. It reports stop when the onboarding must not advance.
func checkSIM(o Onboarding, plan *ServicePlan, f Facts) (Decision, bool) {
	iccid, imsi := strings.TrimSpace(f.ICCID), strings.TrimSpace(f.IMSI)
	needICCID := o.ExpectedICCID != ""
	needIMSI := o.ExpectedIMSI != "" || (plan != nil && len(plan.AllowedPLMNs) > 0)
	if (needICCID && iccid == "") || (needIMSI && imsi == "") {
		return Decision{State: StateContacted, Reason: "waiting for the SIM identifiers", ReadSIM: true}, true
	}
	if needICCID && iccid != strings.TrimSpace(o.ExpectedICCID) {
		return Decision{State: StateOnHold, Reason: fmt.Sprintf("SIM does not match the sale: expected ICCID %s, device reports %s",
			parameters.MaskSubscriberIdentifier(parameters.SubscriberICCID, o.ExpectedICCID),
			parameters.MaskSubscriberIdentifier(parameters.SubscriberICCID, iccid))}, true
	}
	if o.ExpectedIMSI != "" && imsi != strings.TrimSpace(o.ExpectedIMSI) {
		return Decision{State: StateOnHold, Reason: fmt.Sprintf("SIM does not match the sale: expected IMSI %s, device reports %s",
			parameters.MaskSubscriberIdentifier(parameters.SubscriberIMSI, o.ExpectedIMSI),
			parameters.MaskSubscriberIdentifier(parameters.SubscriberIMSI, imsi))}, true
	}
	if plan != nil && len(plan.AllowedPLMNs) > 0 && !plan.AllowsIMSI(imsi) {
		network := imsi
		if len(network) > 5 {
			network = network[:5]
		}
		return Decision{State: StateOnHold, Reason: fmt.Sprintf("SIM belongs to network %s; the plan allows %s",
			network, strings.Join(plan.AllowedPLMNs, ", "))}, true
	}
	return Decision{}, false
}
