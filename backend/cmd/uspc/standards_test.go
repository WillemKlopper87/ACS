package main

import (
	"acs/internal/jobs"
	"acs/internal/usp/uspproto"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestUSPTR143DispatchUsesOperate(t *testing.T) {
	cases := []struct {
		kind                string
		payload             any
		command, key, value string
	}{
		{jobs.TypeDiagnosticsDownload, jobs.DiagnosticsDownloadPayload{URL: "https://test.example/file", USPCommand: "Device.IP.Diagnostics.DownloadDiagnostics()"}, "Device.IP.Diagnostics.DownloadDiagnostics()", "DownloadURL", "https://test.example/file"},
		{jobs.TypeDiagnosticsUpload, jobs.DiagnosticsUploadPayload{URL: "https://test.example/file", USPCommand: "Device.IP.Diagnostics.UploadDiagnostics()", TestFileLength: 4096}, "Device.IP.Diagnostics.UploadDiagnostics()", "TestFileLength", "4096"},
	}
	for _, c := range cases {
		job := mustJob(t, c.kind, c.payload)
		raw, err := buildUSPRequest("test", job)
		if err != nil {
			t.Fatal(err)
		}
		var msg uspproto.Msg
		if err := proto.Unmarshal(raw, &msg); err != nil {
			t.Fatal(err)
		}
		op := msg.GetBody().GetRequest().GetOperate()
		if op.GetCommand() != c.command || op.GetInputArgs()[c.key] != c.value || !op.GetSendResp() {
			t.Fatalf("operate = %+v", op)
		}
	}
	if _, err := buildUSPRequest("test", mustJob(t, jobs.TypeDiagnosticsDownload, jobs.DiagnosticsDownloadPayload{URL: "https://test.example/file"})); err != ErrUnsupportedOverUSP {
		t.Fatal("CWMP-only job should not dispatch over USP")
	}
}
func TestUSPDiscoveryRetainsCommandOnlyObjects(t *testing.T) {
	outcome := classifyGetSupportedDMResp("GET_SUPPORTED_DM_RESP", &uspproto.GetSupportedDMResp{ReqObjResults: []*uspproto.GetSupportedDMResp_RequestedObjectResult{{ReqObjPath: "Device.", SupportedObjs: []*uspproto.GetSupportedDMResp_SupportedObjectResult{{SupportedObjPath: "Device.IP.Diagnostics.", SupportedCommands: []*uspproto.GetSupportedDMResp_SupportedCommandResult{{CommandName: "DownloadDiagnostics()"}}}}}}})
	if _, ok := outcome.discoveredNames["Device.IP.Diagnostics.DownloadDiagnostics()"]; !ok {
		t.Fatal("command lost")
	}
	if len(outcome.discoveredCapabilities) != 1 || len(outcome.discoveredCapabilities[0].Commands) != 1 {
		t.Fatal("command metadata lost")
	}
}

func TestUSPDiscoveryRejectsPartialErrors(t *testing.T) {
	outcome := classifyGetSupportedDMResp("GET_SUPPORTED_DM_RESP", &uspproto.GetSupportedDMResp{ReqObjResults: []*uspproto.GetSupportedDMResp_RequestedObjectResult{{ReqObjPath: "Device.Cellular.", ErrCode: 7016, ErrMsg: "unsupported object"}}})
	if outcome.uspErr == nil {
		t.Fatal("partial discovery must not replace complete evidence")
	}
}

func TestUSPIncrementalDiscoveryUsesCursor(t *testing.T) {
	raw, err := buildUSPRequest("test", mustJob(t, jobs.TypeParameterDiscovery, jobs.ParameterDiscoveryPayload{Root: "Device.", Cursor: "Device.Cellular.", Incremental: true}))
	if err != nil {
		t.Fatal(err)
	}
	var msg uspproto.Msg
	if err := proto.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	request := msg.GetBody().GetRequest().GetGetSupportedDm()
	if !request.GetFirstLevelOnly() || len(request.GetObjPaths()) != 1 || request.GetObjPaths()[0] != "Device.Cellular." {
		t.Fatalf("unexpected request: %v", request)
	}
}
