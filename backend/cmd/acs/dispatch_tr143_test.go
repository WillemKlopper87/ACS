package main

import (
	"testing"

	"acs/internal/cwmp"
)

func tr143Params(pairs ...string) []cwmp.ParameterValueStruct {
	var out []cwmp.ParameterValueStruct
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, cwmp.ParameterValueStruct{Name: pairs[i], Value: pairs[i+1]})
	}
	return out
}

// TestComputeTR143ResultDerivesThroughput covers the core case: a CPE that
// populated BOM/EOM and the test byte counter gets a computed bps figure,
// not just raw timestamps a caller would have to parse and subtract
// themselves.
func TestComputeTR143ResultDerivesThroughput(t *testing.T) {
	const prefix = "Device.IP.Diagnostics.DownloadDiagnostics."
	list := tr143Params(
		prefix+"ROMTime", "2026-09-18T10:00:00Z",
		prefix+"BOMTime", "2026-09-18T10:00:01Z",
		prefix+"EOMTime", "2026-09-18T10:00:03Z",
		prefix+"TestBytesReceived", "2500000",
	)
	got := computeTR143Result(list, prefix, false)
	if got.Direction != "download" {
		t.Errorf("direction = %q, want download", got.Direction)
	}
	if got.TestBytes != 2500000 {
		t.Errorf("test bytes = %d, want 2500000", got.TestBytes)
	}
	if got.TestDurationMS != 2000 {
		t.Errorf("duration = %dms, want 2000ms (EOM-BOM)", got.TestDurationMS)
	}
	if want := int64(10000000); got.ThroughputBps != want {
		t.Errorf("throughput = %d bps, want %d (2.5MB over 2s = 10Mbps)", got.ThroughputBps, want)
	}
}

// TestComputeTR143ResultUploadUsesTestBytesSent proves the upload direction
// reads TestBytesSent, not TestBytesReceived -- the two are genuinely
// different result parameters per TR-143, and swapping them would silently
// report zero throughput for every upload test.
func TestComputeTR143ResultUploadUsesTestBytesSent(t *testing.T) {
	const prefix = "Device.IP.Diagnostics.UploadDiagnostics."
	list := tr143Params(
		prefix+"BOMTime", "2026-09-18T10:00:00Z",
		prefix+"EOMTime", "2026-09-18T10:00:01Z",
		prefix+"TestBytesSent", "1000000",
		prefix+"TestBytesReceived", "0",
	)
	got := computeTR143Result(list, prefix, true)
	if got.Direction != "upload" || got.TestBytes != 1000000 {
		t.Fatalf("got = %+v, want direction=upload test_bytes=1000000", got)
	}
}

// TestComputeTR143ResultToleratesMissingTimestamps covers a CPE that only
// supports the mandatory subset and never populates BOM/EOM: the raw byte
// counters must still come through, without a fabricated or divide-by-zero
// throughput.
func TestComputeTR143ResultToleratesMissingTimestamps(t *testing.T) {
	const prefix = "Device.IP.Diagnostics.DownloadDiagnostics."
	list := tr143Params(prefix + "TestBytesReceived", "500")
	got := computeTR143Result(list, prefix, false)
	if got.TestBytes != 500 {
		t.Errorf("test bytes = %d, want 500", got.TestBytes)
	}
	if got.ThroughputBps != 0 || got.TestDurationMS != 0 {
		t.Errorf("got = %+v, want zero throughput/duration when BOM/EOM are absent", got)
	}
}

// TestComputeTR143ResultTreatsZeroDateAsAbsent covers TR-069's own
// "unknown time" convention (0001-01-01T00:00:00Z) -- must not be treated
// as a real instant, which would otherwise compute a nonsensical duration
// spanning millennia.
func TestComputeTR143ResultTreatsZeroDateAsAbsent(t *testing.T) {
	const prefix = "Device.IP.Diagnostics.DownloadDiagnostics."
	list := tr143Params(
		prefix+"BOMTime", "0001-01-01T00:00:00Z",
		prefix+"EOMTime", "2026-09-18T10:00:03Z",
		prefix+"TestBytesReceived", "1000",
	)
	got := computeTR143Result(list, prefix, false)
	if got.ThroughputBps != 0 || got.TestDurationMS != 0 {
		t.Errorf("got = %+v, want zero throughput when BOMTime is the TR-069 unknown-time sentinel", got)
	}
}

// TestComputeTR143ResultDerivesTCPOpenTime covers the new poll addition:
// TCPOpenRequestTime/TCPOpenResponseTime, absent from the poll before this
// change, now surface as a derived connection-setup latency.
func TestComputeTR143ResultDerivesTCPOpenTime(t *testing.T) {
	const prefix = "Device.IP.Diagnostics.DownloadDiagnostics."
	list := tr143Params(
		prefix+"TCPOpenRequestTime", "2026-09-18T10:00:00.000Z",
		prefix+"TCPOpenResponseTime", "2026-09-18T10:00:00.120Z",
		prefix+"TestBytesReceived", "100",
	)
	got := computeTR143Result(list, prefix, false)
	if got.TCPOpenTimeMS != 120 {
		t.Errorf("tcp open time = %dms, want 120ms", got.TCPOpenTimeMS)
	}
}

// TestParseCWMPDateTimeRejectsGarbage guards computeTR143Result against a
// malformed timestamp being silently treated as a zero-value valid instant.
func TestParseCWMPDateTimeRejectsGarbage(t *testing.T) {
	if _, ok := parseCWMPDateTime("not-a-timestamp"); ok {
		t.Fatal("expected an unparseable timestamp to be rejected")
	}
	if _, ok := parseCWMPDateTime(""); ok {
		t.Fatal("expected an empty timestamp to be rejected")
	}
}

// TestDiagTR143PollPathsIncludesTCPOpenTimes locks in the poll-path
// addition itself -- without it, computeTR143Result would never see
// TCPOpenRequestTime/TCPOpenResponseTime in the first place, regardless of
// whether the CPE supports them.
func TestDiagTR143PollPathsIncludesTCPOpenTimes(t *testing.T) {
	paths := diagTR143PollPaths("Device.IP.Diagnostics.DownloadDiagnostics.")
	want := map[string]bool{
		"Device.IP.Diagnostics.DownloadDiagnostics.TCPOpenRequestTime":  false,
		"Device.IP.Diagnostics.DownloadDiagnostics.TCPOpenResponseTime": false,
	}
	for _, p := range paths {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Errorf("diagTR143PollPaths missing %q", path)
		}
	}
}
