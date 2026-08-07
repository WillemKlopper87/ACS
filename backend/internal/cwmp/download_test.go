package cwmp

import (
	"strings"
	"testing"
)

func TestRenderDownload(t *testing.T) {
	raw := RenderDownload("acs-test-id", "fw_20260804_0001", "1 Firmware Upgrade Image",
		"http://localhost:8080/api/v1/firmware/images/abc123/file", "", "", 52428800, "firmware.bin", 0)
	mustWellFormed(t, raw)

	for _, want := range []string{
		"<CommandKey>fw_20260804_0001</CommandKey>",
		"<FileType>1 Firmware Upgrade Image</FileType>",
		"<URL>http://localhost:8080/api/v1/firmware/images/abc123/file</URL>",
		"<FileSize>52428800</FileSize>",
		"<TargetFileName>firmware.bin</TargetFileName>",
		"<DelaySeconds>0</DelaySeconds>",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("expected rendered Download to contain %q, got: %s", want, raw)
		}
	}
}

func TestParseDownloadResponse(t *testing.T) {
	env, err := ParseEnvelope(readFixture(t, "download_response.xml"))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Body.DownloadResponse == nil {
		t.Fatal("expected DownloadResponse, got nil")
	}
	if got, want := env.Body.DownloadResponse.Status, 1; got != want {
		t.Errorf("Status = %d, want %d", got, want)
	}
}

func TestParseTransferCompleteSuccess(t *testing.T) {
	env, err := ParseEnvelope(readFixture(t, "transfer_complete.xml"))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	tc := env.Body.TransferComplete
	if tc == nil {
		t.Fatal("expected TransferComplete, got nil")
	}
	if got, want := tc.CommandKey, "fw_20260804_test0001"; got != want {
		t.Errorf("CommandKey = %q, want %q", got, want)
	}
	// The critical case this test exists for: FaultStruct is present (per
	// spec, always is) with FaultCode 0, which means success, not failure.
	if tc.IsFault() {
		t.Error("IsFault() = true for FaultCode 0, want false — FaultStruct presence alone must not signal failure")
	}
}

func TestParseTransferCompleteFault(t *testing.T) {
	env, err := ParseEnvelope(readFixture(t, "transfer_complete_fault.xml"))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	tc := env.Body.TransferComplete
	if tc == nil {
		t.Fatal("expected TransferComplete, got nil")
	}
	if !tc.IsFault() {
		t.Error("IsFault() = false for FaultCode 9016, want true")
	}
	if got, want := tc.FaultStruct.FaultCode, "9016"; got != want {
		t.Errorf("FaultCode = %q, want %q", got, want)
	}
}

func TestRenderTransferCompleteResponse(t *testing.T) {
	raw := RenderTransferCompleteResponse("acs-test-id")
	mustWellFormed(t, raw)
	if !strings.Contains(string(raw), "TransferCompleteResponse") {
		t.Errorf("expected TransferCompleteResponse element, got: %s", raw)
	}
}
