package diagnostics

import (
	"errors"
	"strconv"
	"strings"

	"acs/internal/jobs"
)

// Interactive and scheduled diagnostics share these defaults so a schedule
// that only names a host does not send NumberOfRepetitions=0 (fault 9007 on
// strict CPEs, a no-op test on lenient ones).
const (
	PingDefaultRepetitions = 4
	PingDefaultTimeoutMS   = 5000
	PingDefaultBlockSize   = 64

	TracerouteDefaultTries     = 3
	TracerouteDefaultTimeoutMS = 5000
	TracerouteDefaultBlockSize = 38
	TracerouteDefaultMaxHops   = 30
)

// ApplyPingDefaults fills every zero-valued optional IPPing input. DSCP 0 is
// the standard default and is left alone.
func ApplyPingDefaults(p *jobs.DiagnosticsPingPayload) {
	if p.NumberOfRepetitions == 0 {
		p.NumberOfRepetitions = PingDefaultRepetitions
	}
	if p.Timeout == 0 {
		p.Timeout = PingDefaultTimeoutMS
	}
	if p.DataBlockSize == 0 {
		p.DataBlockSize = PingDefaultBlockSize
	}
}

// ApplyTracerouteDefaults is ApplyPingDefaults for TraceRoute.
func ApplyTracerouteDefaults(p *jobs.DiagnosticsTraceroutePayload) {
	if p.NumberOfTries == 0 {
		p.NumberOfTries = TracerouteDefaultTries
	}
	if p.Timeout == 0 {
		p.Timeout = TracerouteDefaultTimeoutMS
	}
	if p.DataBlockSize == 0 {
		p.DataBlockSize = TracerouteDefaultBlockSize
	}
	if p.MaxHopCount == 0 {
		p.MaxHopCount = TracerouteDefaultMaxHops
	}
}

// leafTypes are the TR-181 (and, with the same leaf names, TR-098) syntax of
// every diagnostic input the ACS writes. Without an explicit type the SPV
// renderer falls back to xsd:string, which strict CPEs reject with 9006 for
// numeric leaves. TestLeafTypesMatchTR181Dictionary pins these to the
// bundled BBF dictionary.
var leafTypes = map[string]string{
	"Host":                "xsd:string",
	"DiagnosticsState":    "xsd:string",
	"NumberOfRepetitions": "xsd:unsignedInt",
	"NumberOfTries":       "xsd:unsignedInt",
	"Timeout":             "xsd:unsignedInt",
	"DataBlockSize":       "xsd:unsignedInt",
	"DSCP":                "xsd:unsignedInt",
	"MaxHopCount":         "xsd:unsignedInt",
	"DownloadURL":         "xsd:string",
	"UploadURL":           "xsd:string",
	"TestFileLength":      "xsd:unsignedLong",
}

// LeafType returns the xsd type for a diagnostic input leaf ("" if unknown).
func LeafType(leaf string) string { return leafTypes[leaf] }

// Result leaves read back by a poll, in addition to DiagnosticsState.
var (
	PingResultLeaves       = []string{"SuccessCount", "FailureCount", "AverageResponseTime", "MinimumResponseTime", "MaximumResponseTime"}
	TracerouteResultLeaves = []string{"ResponseTime", "RouteHopsNumberOfEntries"}
	tr143CommonLeaves      = []string{"ROMTime", "BOMTime", "EOMTime", "TotalBytesReceived", "TotalBytesSent", "TCPOpenRequestTime", "TCPOpenResponseTime"}
)

// TR143ResultLeaves returns the result leaves for one TR-143 direction:
// only Download has TestBytesReceived and only Upload has TestBytesSent (the
// FastMile omits the other one, so polling both faults 9005).
func TR143ResultLeaves(upload bool) []string {
	test := "TestBytesReceived"
	if upload {
		test = "TestBytesSent"
	}
	return append([]string{test}, tr143CommonLeaves...)
}

// ErrNotDiscovered means the device has no discovered parameter tree, so
// nothing proves it supports any diagnostic.
var ErrNotDiscovered = errors.New("device parameters have not been discovered; run parameter discovery first")

// UnsupportedError explains why discovery rules a diagnostic out.
type UnsupportedError struct {
	Diagnostic string
	Reason     string
}

func (e *UnsupportedError) Error() string {
	return "device does not support " + e.Diagnostic + ": " + e.Reason
}

// Input is one candidate input leaf (relative to the diagnostic's prefix)
// and the value the ACS would write.
type Input struct {
	Leaf     string
	Value    string
	Required bool
}

// Plan is the outcome of planning one diagnostic against discovery.
type Plan struct {
	jobs.DiagnosticsPlan
	// USP is set when the device advertises the USP command form (e.g.
	// "Device.IP.Diagnostics.IPPing()"); cmd/uspc renders the Operate from
	// the payload itself, so no CWMP wire plan is needed.
	USP bool
	// Skipped lists optional inputs discovery did not prove writable; the
	// CPE's own defaults apply to them.
	Skipped []string
}

// PlanCWMP builds the trigger/poll plan for the diagnostic object at prefix
// (e.g. "Device.IP.Diagnostics.IPPing.") from the device's discovered names
// (path -> writable). Required inputs and DiagnosticsState must be
// discovered writable; optional inputs are sent only when discovered
// writable; poll paths only name leaves discovery found.
func PlanCWMP(names map[string]bool, diagnostic, prefix string, inputs []Input, resultLeaves []string) (Plan, error) {
	if names == nil {
		return Plan{}, ErrNotDiscovered
	}
	if _, usp := names[strings.TrimSuffix(prefix, ".")+"()"]; usp {
		return Plan{USP: true}, nil
	}
	if !objectDiscovered(names, prefix) {
		return Plan{}, &UnsupportedError{Diagnostic: diagnostic, Reason: prefix + " was not found by parameter discovery"}
	}
	var plan Plan
	for _, in := range inputs {
		writable, present := names[prefix+in.Leaf]
		if !present || !writable {
			if in.Required {
				return Plan{}, &UnsupportedError{Diagnostic: diagnostic, Reason: prefix + in.Leaf + " is not a discovered writable parameter"}
			}
			plan.Skipped = append(plan.Skipped, in.Leaf)
			continue
		}
		plan.Trigger = append(plan.Trigger, jobs.ParameterWrite{Name: prefix + in.Leaf, Value: in.Value, Type: LeafType(in.Leaf)})
	}
	if writable, present := names[prefix+"DiagnosticsState"]; !present || !writable {
		return Plan{}, &UnsupportedError{Diagnostic: diagnostic, Reason: prefix + "DiagnosticsState is not a discovered writable parameter"}
	}
	plan.Trigger = append(plan.Trigger, jobs.ParameterWrite{Name: prefix + "DiagnosticsState", Value: "Requested", Type: LeafType("DiagnosticsState")})
	plan.PollPaths = []string{prefix + "DiagnosticsState"}
	for _, leaf := range resultLeaves {
		if _, present := names[prefix+leaf]; present {
			plan.PollPaths = append(plan.PollPaths, prefix+leaf)
		}
	}
	return plan, nil
}

func objectDiscovered(names map[string]bool, prefix string) bool {
	if _, ok := names[prefix]; ok {
		return true
	}
	if _, ok := names[strings.TrimSuffix(prefix, ".")]; ok {
		return true
	}
	for name := range names {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func itoa(n int) string { return strconv.Itoa(n) }

// PlanPing resolves an IPPing payload (defaults already applied) against
// discovery and stores the plan on it.
func PlanPing(names map[string]bool, p *jobs.DiagnosticsPingPayload) (Plan, error) {
	plan, err := PlanCWMP(names, "IPPing", p.Prefix, []Input{
		{Leaf: "Host", Value: p.Host, Required: true},
		{Leaf: "NumberOfRepetitions", Value: itoa(p.NumberOfRepetitions)},
		{Leaf: "Timeout", Value: itoa(p.Timeout)},
		{Leaf: "DataBlockSize", Value: itoa(p.DataBlockSize)},
		{Leaf: "DSCP", Value: itoa(p.DSCP)},
	}, PingResultLeaves)
	if err == nil {
		p.DiagnosticsPlan = plan.DiagnosticsPlan
	}
	return plan, err
}

// PlanTraceroute is PlanPing for TraceRoute.
func PlanTraceroute(names map[string]bool, p *jobs.DiagnosticsTraceroutePayload) (Plan, error) {
	plan, err := PlanCWMP(names, "TraceRoute", p.Prefix, []Input{
		{Leaf: "Host", Value: p.Host, Required: true},
		{Leaf: "NumberOfTries", Value: itoa(p.NumberOfTries)},
		{Leaf: "Timeout", Value: itoa(p.Timeout)},
		{Leaf: "DataBlockSize", Value: itoa(p.DataBlockSize)},
		{Leaf: "DSCP", Value: itoa(p.DSCP)},
		{Leaf: "MaxHopCount", Value: itoa(p.MaxHopCount)},
	}, TracerouteResultLeaves)
	if err == nil {
		p.DiagnosticsPlan = plan.DiagnosticsPlan
	}
	return plan, err
}

// PlanTR143 plans a TR-143 Download (upload=false) or Upload test at prefix.
func PlanTR143(names map[string]bool, prefix, url string, upload bool, testFileLength uint64) (Plan, error) {
	diagnostic, inputs := "DownloadDiagnostics", []Input{{Leaf: "DownloadURL", Value: url, Required: true}}
	if upload {
		diagnostic = "UploadDiagnostics"
		inputs = []Input{{Leaf: "UploadURL", Value: url, Required: true}, {Leaf: "TestFileLength", Value: strconv.FormatUint(testFileLength, 10)}}
	}
	return PlanCWMP(names, diagnostic, prefix, inputs, TR143ResultLeaves(upload))
}

// Capability is the console-facing summary for one diagnostic.
type Capability struct {
	Supported bool   `json:"supported"`
	Path      string `json:"path,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// PingTracerouteCapabilities reports whether IPPing and TraceRoute can be
// run, using the same rules the create endpoints enforce.
func PingTracerouteCapabilities(names map[string]bool, pingPrefix, traceroutePrefix string) (ping, traceroute Capability) {
	ping = capabilityOf(PlanPing(names, &jobs.DiagnosticsPingPayload{Host: "x", Prefix: pingPrefix}))
	ping.Path = pingPrefix
	traceroute = capabilityOf(PlanTraceroute(names, &jobs.DiagnosticsTraceroutePayload{Host: "x", Prefix: traceroutePrefix}))
	traceroute.Path = traceroutePrefix
	return ping, traceroute
}

func capabilityOf(_ Plan, err error) Capability {
	if err == nil {
		return Capability{Supported: true}
	}
	var unsupported *UnsupportedError
	if errors.As(err, &unsupported) {
		return Capability{Reason: unsupported.Reason}
	}
	return Capability{Reason: err.Error()}
}
