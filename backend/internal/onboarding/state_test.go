package onboarding

import (
	"strings"
	"testing"
)

func planned() Onboarding {
	return Onboarding{ID: "ob-1", ExpectedSerial: "CSC25430018F", State: StatePlanned}
}

func TestEvaluateHappyPath(t *testing.T) {
	o := planned()
	plan := &ServicePlan{RequireDiscovery: true, TemplateIDs: []string{"tpl-1"}}

	d := Evaluate(o, plan, Facts{Informed: true})
	if d.State != StateContacted || d.Configure {
		t.Fatalf("first Inform: %+v, want CONTACTED", d)
	}
	o.State = d.State

	d = Evaluate(o, plan, Facts{Informed: true, Secured: true})
	if d.State != StateSecured {
		t.Fatalf("own credential active: %+v, want SECURED", d)
	}
	o.State = d.State

	// Discovered: configured in the same pass, not on the next Inform.
	d = Evaluate(o, plan, Facts{Informed: true, Secured: true, Discovered: true})
	if d.State != StateConfiguring || !d.Configure {
		t.Fatalf("plan applies: %+v, want CONFIGURING with configuration queued", d)
	}
	o.State, o.ConfigJobs = d.State, []string{"set_1"}

	d = Evaluate(o, plan, Facts{Informed: true, Secured: true, Discovered: true, ConfigPending: 1})
	if d.State != StateConfiguring {
		t.Fatalf("jobs pending: %+v, want to stay CONFIGURING", d)
	}
	d = Evaluate(o, plan, Facts{Informed: true, Secured: true, Discovered: true, ConfigDone: 1})
	if d.State != StateInService {
		t.Fatalf("jobs verified: %+v, want IN_SERVICE", d)
	}
}

func TestEvaluateSkipsWhatThePlanDoesNotNeed(t *testing.T) {
	o := planned()
	o.State = StateSecured
	// No discovery required and nothing to configure: straight to service.
	d := Evaluate(o, &ServicePlan{RequireDiscovery: false}, Facts{Informed: true, Secured: true})
	if d.State != StateInService || d.Configure {
		t.Fatalf("%+v, want IN_SERVICE with nothing to apply", d)
	}
	// A template-only plan that skips discovery configures at once.
	if d := Evaluate(o, &ServicePlan{TemplateIDs: []string{"t"}}, Facts{Informed: true, Secured: true}); d.State != StateConfiguring || !d.Configure {
		t.Fatalf("%+v, want CONFIGURING without discovery", d)
	}
	o.State = StateDiscovered
	if d := Evaluate(o, nil, Facts{Informed: true, Secured: true}); d.State != StateInService {
		t.Fatalf("no plan: %+v, want IN_SERVICE", d)
	}
}

func TestEvaluateHoldsOnSIMMismatch(t *testing.T) {
	o := planned()
	o.State = StateContacted
	o.ExpectedICCID = "89270000000000001234"

	d := Evaluate(o, nil, Facts{Informed: true, Secured: true})
	if d.State != StateContacted || !d.ReadSIM || !strings.Contains(d.Reason, "SIM") {
		t.Fatalf("SIM unknown: %+v, want a SIM read and to wait", d)
	}
	d = Evaluate(o, nil, Facts{Informed: true, Secured: true, ICCID: "89270000000000009999"})
	if d.State != StateOnHold || !strings.Contains(d.Reason, "does not match the sale") {
		t.Fatalf("wrong SIM: %+v, want ON_HOLD", d)
	}
	if strings.Contains(d.Reason, "89270000000000009999") || strings.Contains(d.Reason, "89270000000000001234") {
		t.Fatalf("reason exposes a full ICCID: %q", d.Reason)
	}
	// Right SIM: it advances as far as the facts allow in one pass
	// (no plan: nothing to discover or configure).
	if d := Evaluate(o, nil, Facts{Informed: true, Secured: true, ICCID: " 89270000000000001234 "}); d.State != StateInService {
		t.Fatalf("right SIM: %+v, want past the SIM check to IN_SERVICE", d)
	}
}

func TestEvaluateHoldsOnForeignNetwork(t *testing.T) {
	o := planned()
	o.State = StateContacted
	plan := &ServicePlan{AllowedPLMNs: []string{"65506"}}
	if d := Evaluate(o, plan, Facts{Informed: true, Secured: true, IMSI: "655100000000013"}); d.State != StateOnHold || !strings.Contains(d.Reason, "65510") {
		t.Fatalf("foreign SIM: %+v, want ON_HOLD naming the network", d)
	}
	if d := Evaluate(o, plan, Facts{Informed: true, Secured: true, IMSI: "655060000000013"}); d.State != StateInService {
		t.Fatalf("own network: %+v, want past the network check to IN_SERVICE", d)
	}
	if d := Evaluate(o, plan, Facts{Informed: true, Secured: true}); d.State != StateContacted || !d.ReadSIM {
		t.Fatalf("IMSI unknown: %+v, want a SIM read", d)
	}
}

func TestEvaluateFailsOnConfigFailureAndLeavesTerminalStatesAlone(t *testing.T) {
	o := planned()
	o.State, o.ConfigJobs = StateConfiguring, []string{"set_1", "set_2"}
	d := Evaluate(o, &ServicePlan{TemplateIDs: []string{"t"}}, Facts{Informed: true, Secured: true, Discovered: true, ConfigDone: 1, ConfigFailed: 1, ConfigError: "fault 9007"})
	if d.State != StateFailed || !strings.Contains(d.Reason, "fault 9007") {
		t.Fatalf("%+v, want FAILED with the job's fault", d)
	}
	for _, s := range []string{StateInService, StateCancelled, StateFailed, StateOnHold} {
		o.State = s
		if d := Evaluate(o, nil, Facts{Informed: true, Secured: true, Discovered: true}); d.State != s || d.Configure || d.ReadSIM {
			t.Fatalf("%s moved: %+v", s, d)
		}
	}
	// Planned but not yet seen: nothing happens.
	if d := Evaluate(planned(), nil, Facts{}); d.State != StatePlanned {
		t.Fatalf("unseen device moved: %+v", d)
	}
}
