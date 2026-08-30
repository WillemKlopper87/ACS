package jobs

import (
	"regexp"
	"testing"
)

var commandKeyPattern = regexp.MustCompile(`^[a-z]+_[0-9]{8}_[0-9a-f]{8}$`)

func TestNewCommandKeyPrefixPerJobType(t *testing.T) {
	cases := map[string]string{
		TypeSetParameter:           "setparam",
		TypeGetParameter:           "getparam",
		TypeConnectionRequest:      "cr",
		TypeFirmwareDownload:       "fw",
		TypeDiagnosticsPing:        "diag",
		TypeDiagnosticsTraceroute:  "trace",
		TypeAddObject:              "addobj",
		TypeDeleteObject:           "delobj",
		TypeReboot:                 "reboot",
		TypeFactoryReset:           "reset",
		TypeScheduleInform:         "schedinform",
		TypeSetParameterAttributes: "setattr",
		TypeGetParameterAttributes: "getattr",
		TypeUpload:                 "upload",
		TypeParameterDiscovery:     "discover",
		"SOME_UNKNOWN_TYPE":        "job",
	}
	for jobType, wantPrefix := range cases {
		key := newCommandKey(jobType)
		if !commandKeyPattern.MatchString(key) {
			t.Errorf("newCommandKey(%q) = %q, does not match expected shape prefix_YYYYMMDD_hexhexhexhex", jobType, key)
			continue
		}
		gotPrefix := key[:len(key)-len("_20260804_deadbeef")]
		if gotPrefix != wantPrefix {
			t.Errorf("newCommandKey(%q) prefix = %q, want %q (full key: %q)", jobType, gotPrefix, wantPrefix, key)
		}
	}
}

func TestNewCommandKeyUniqueAcrossCalls(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		key := newCommandKey(TypeSetParameter)
		if seen[key] {
			t.Fatalf("newCommandKey produced a duplicate after %d calls: %q", i, key)
		}
		seen[key] = true
	}
}
