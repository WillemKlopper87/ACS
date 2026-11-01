package diagnostics

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"testing"

	"acs/internal/datamodel"
	"acs/internal/jobs"
)

// discoveredNames loads a committed compatibility discovery (path, RO/RW)
// into the path->writable map parameter discovery stores.
func discoveredNames(t *testing.T, rel string) map[string]bool {
	t.Helper()
	f, err := os.Open("../../../ci/compatibility/discoveries/" + rel)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	names := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		names[fields[0]] = len(fields) > 1 && fields[1] == "RW"
	}
	return names
}

const (
	fastMile = "Nokia/3TG02241/1.2501.00.0798/parameters.tsv"
	beacon   = "ALCL/Beacon-3.1/3TN00626IJLJ03/parameters.tsv"
)

func triggerTypes(plan jobs.DiagnosticsPlan) map[string]string {
	out := map[string]string{}
	for _, p := range plan.Trigger {
		out[p.Name[strings.LastIndex(p.Name, ".")+1:]] = p.Type
	}
	return out
}

func TestPlanPingFastMileSendsTypedInputs(t *testing.T) {
	p := jobs.DiagnosticsPingPayload{Host: "8.8.8.8", Prefix: "Device.IP.Diagnostics.IPPing."}
	ApplyPingDefaults(&p)
	if _, err := PlanPing(discoveredNames(t, fastMile), &p); err != nil {
		t.Fatal(err)
	}
	got := triggerTypes(p.DiagnosticsPlan)
	want := map[string]string{"Host": "xsd:string", "NumberOfRepetitions": "xsd:unsignedInt", "Timeout": "xsd:unsignedInt",
		"DataBlockSize": "xsd:unsignedInt", "DSCP": "xsd:unsignedInt", "DiagnosticsState": "xsd:string"}
	if len(got) != len(want) {
		t.Fatalf("trigger = %+v, want leaves %v", p.Trigger, want)
	}
	for leaf, typ := range want {
		if got[leaf] != typ {
			t.Errorf("%s type = %q, want %q", leaf, got[leaf], typ)
		}
	}
	if last := p.Trigger[len(p.Trigger)-1]; last.Name != p.Prefix+"DiagnosticsState" || last.Value != "Requested" {
		t.Errorf("last trigger write = %+v, want DiagnosticsState=Requested", last)
	}
	for _, w := range p.Trigger {
		if w.Name == p.Prefix+"NumberOfRepetitions" && w.Value != "4" {
			t.Errorf("NumberOfRepetitions = %q, want default 4", w.Value)
		}
	}
	if len(p.PollPaths) != 1+len(PingResultLeaves) {
		t.Errorf("poll paths = %v, want DiagnosticsState plus every ping result leaf", p.PollPaths)
	}
}

// The FastMile's TraceRoute has no Timeout, DSCP or ResponseTime leaves:
// writing or reading them faults 9005 for the whole RPC.
func TestPlanTracerouteFastMileOmitsUndiscoveredLeaves(t *testing.T) {
	p := jobs.DiagnosticsTraceroutePayload{Host: "8.8.8.8", Prefix: "Device.IP.Diagnostics.TraceRoute."}
	ApplyTracerouteDefaults(&p)
	plan, err := PlanTraceroute(discoveredNames(t, fastMile), &p)
	if err != nil {
		t.Fatal(err)
	}
	got := triggerTypes(p.DiagnosticsPlan)
	for _, absent := range []string{"Timeout", "DSCP"} {
		if _, ok := got[absent]; ok {
			t.Errorf("trigger writes undiscovered %s", absent)
		}
	}
	if got["MaxHopCount"] != "xsd:unsignedInt" || got["NumberOfTries"] != "xsd:unsignedInt" {
		t.Errorf("trigger types = %v", got)
	}
	if strings.Join(plan.Skipped, ",") != "Timeout,DSCP" {
		t.Errorf("skipped = %v, want Timeout,DSCP", plan.Skipped)
	}
	for _, path := range p.PollPaths {
		if strings.HasSuffix(path, ".ResponseTime") {
			t.Errorf("poll reads undiscovered %s", path)
		}
	}
}

func TestPlanTR143FastMilePollsOnlyItsDirection(t *testing.T) {
	names := discoveredNames(t, fastMile)
	down, err := PlanTR143(names, "Device.IP.Diagnostics.DownloadDiagnostics.", "https://speed.example.com/f", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range down.PollPaths {
		if strings.HasSuffix(path, "TestBytesSent") {
			t.Errorf("download poll reads %s, which the FastMile does not have", path)
		}
	}
	up, err := PlanTR143(names, "Device.IP.Diagnostics.UploadDiagnostics.", "https://speed.example.com/u", true, 1048576)
	if err != nil {
		t.Fatal(err)
	}
	if got := triggerTypes(up.DiagnosticsPlan); got["TestFileLength"] != "xsd:unsignedLong" || got["UploadURL"] != "xsd:string" {
		t.Errorf("upload trigger types = %v", got)
	}
}

func TestPlanBeaconHasNoDiagnostics(t *testing.T) {
	names := discoveredNames(t, beacon)
	p := jobs.DiagnosticsPingPayload{Host: "8.8.8.8", Prefix: "Device.IP.Diagnostics.IPPing."}
	_, err := PlanPing(names, &p)
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want UnsupportedError", err)
	}
	ping, trace := PingTracerouteCapabilities(names, "Device.IP.Diagnostics.IPPing.", "Device.IP.Diagnostics.TraceRoute.")
	if ping.Supported || trace.Supported || ping.Reason == "" {
		t.Errorf("capabilities = %+v %+v, want both unsupported with a reason", ping, trace)
	}
}

func TestPlanRequiresDiscovery(t *testing.T) {
	p := jobs.DiagnosticsPingPayload{Host: "8.8.8.8", Prefix: "Device.IP.Diagnostics.IPPing."}
	if _, err := PlanPing(nil, &p); !errors.Is(err, ErrNotDiscovered) {
		t.Fatalf("err = %v, want ErrNotDiscovered", err)
	}
}

func TestPlanRequiresWritableHostAndState(t *testing.T) {
	prefix := "Device.IP.Diagnostics.IPPing."
	names := map[string]bool{prefix: false, prefix + "Host": false, prefix + "DiagnosticsState": true}
	p := jobs.DiagnosticsPingPayload{Host: "8.8.8.8", Prefix: prefix}
	if _, err := PlanPing(names, &p); err == nil || !strings.Contains(err.Error(), "Host") {
		t.Fatalf("err = %v, want read-only Host rejected", err)
	}
}

func TestPlanUSPCommandNeedsNoWirePlan(t *testing.T) {
	p := jobs.DiagnosticsPingPayload{Host: "8.8.8.8", Prefix: "Device.IP.Diagnostics.IPPing."}
	plan, err := PlanPing(map[string]bool{"Device.IP.Diagnostics.IPPing()": false}, &p)
	if err != nil || !plan.USP || len(p.Trigger) != 0 {
		t.Fatalf("plan = %+v err = %v, want USP with no CWMP plan", plan, err)
	}
}

func TestApplyPingDefaultsFillsScheduledHostOnlyPayload(t *testing.T) {
	p := jobs.DiagnosticsPingPayload{Host: "8.8.8.8"}
	ApplyPingDefaults(&p)
	if p.NumberOfRepetitions != PingDefaultRepetitions || p.Timeout != PingDefaultTimeoutMS || p.DataBlockSize != PingDefaultBlockSize {
		t.Fatalf("defaults not applied: %+v", p)
	}
}

// TestLeafTypesMatchTR181Dictionary keeps the explicit wire types in step
// with the bundled BBF TR-181 dictionary.
func TestLeafTypesMatchTR181Dictionary(t *testing.T) {
	for _, version := range []string{"2.20", "2.21"} {
		dict, err := datamodel.Load("cwmp", version)
		if err != nil {
			t.Fatal(err)
		}
		objects := map[string][]string{
			"Device.IP.Diagnostics.IPPing.":              {"Host", "NumberOfRepetitions", "Timeout", "DataBlockSize", "DSCP", "DiagnosticsState"},
			"Device.IP.Diagnostics.TraceRoute.":          {"Host", "NumberOfTries", "Timeout", "DataBlockSize", "DSCP", "MaxHopCount", "DiagnosticsState"},
			"Device.IP.Diagnostics.DownloadDiagnostics.": {"DownloadURL", "DiagnosticsState"},
			"Device.IP.Diagnostics.UploadDiagnostics.":   {"UploadURL", "TestFileLength", "DiagnosticsState"},
		}
		for prefix, leaves := range objects {
			for _, leaf := range leaves {
				entry, ok := dict.Lookup(prefix + leaf)
				if !ok {
					t.Errorf("%s: %s%s missing from dictionary", version, prefix, leaf)
					continue
				}
				if want := "xsd:" + entry.Type; LeafType(leaf) != want {
					t.Errorf("%s: %s%s type = %q, dictionary says %q", version, prefix, leaf, LeafType(leaf), want)
				}
			}
		}
	}
}
