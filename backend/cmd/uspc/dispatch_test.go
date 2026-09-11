package main

import (
	"encoding/json"
	"errors"
	"testing"

	"acs/internal/jobs"
	"acs/internal/usp"
)

func mustJob(t *testing.T, jobType string, payload any) *jobs.Job {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &jobs.Job{
		ID:         "job-1",
		CommandKey: "cmdkey-1",
		DeviceID:   "device-1",
		Type:       jobType,
		Payload:    raw,
	}
}

func TestBuildUSPRequestGetParameter(t *testing.T) {
	job := mustJob(t, jobs.TypeGetParameter, jobs.GetParameterPayload{
		Paths: []string{"Device.DeviceInfo.", "Device.WiFi.SSID.1.SSID"},
	})
	wire, err := buildUSPRequest("m-1", job)
	if err != nil {
		t.Fatalf("buildUSPRequest: %v", err)
	}
	msg, err := usp.DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	get := msg.GetBody().GetRequest().GetGet()
	if get == nil {
		t.Fatal("not a Get message")
	}
	if got := get.GetParamPaths(); len(got) != 2 || got[0] != "Device.DeviceInfo." || got[1] != "Device.WiFi.SSID.1.SSID" {
		t.Errorf("param_paths = %v", got)
	}
	if get.GetMaxDepth() != 0 {
		t.Errorf("max_depth = %d, want 0 (unlimited)", get.GetMaxDepth())
	}
}

func TestBuildUSPRequestSetParameter(t *testing.T) {
	job := mustJob(t, jobs.TypeSetParameter, jobs.SetParameterPayload{
		Parameters: []jobs.ParameterWrite{
			{Name: "Device.WiFi.SSID.1.SSID", Value: "acs-test"},
			{Name: "Device.WiFi.SSID.1.Enable", Value: "true"},
			{Name: "Device.WiFi.SSID.2.SSID", Value: "guest"},
		},
	})
	wire, err := buildUSPRequest("m-2", job)
	if err != nil {
		t.Fatalf("buildUSPRequest: %v", err)
	}
	msg, err := usp.DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	set := msg.GetBody().GetRequest().GetSet()
	if set == nil {
		t.Fatal("not a Set message")
	}
	if set.GetAllowPartial() {
		t.Error("allow_partial = true, want false")
	}
	objs := set.GetUpdateObjs()
	if len(objs) != 2 {
		t.Fatalf("update_objs count = %d, want 2: %+v", len(objs), objs)
	}
	byPath := map[string]map[string]string{}
	for _, o := range objs {
		params := map[string]string{}
		for _, s := range o.GetParamSettings() {
			params[s.GetParam()] = s.GetValue()
			if !s.GetRequired() {
				t.Errorf("param %s.%s required = false, want true", o.GetObjPath(), s.GetParam())
			}
		}
		byPath[o.GetObjPath()] = params
	}
	want := map[string]map[string]string{
		"Device.WiFi.SSID.1.": {"SSID": "acs-test", "Enable": "true"},
		"Device.WiFi.SSID.2.": {"SSID": "guest"},
	}
	if len(byPath) != len(want) {
		t.Fatalf("byPath = %+v, want %+v", byPath, want)
	}
	for path, params := range want {
		gotParams, ok := byPath[path]
		if !ok {
			t.Fatalf("missing object path %q in %+v", path, byPath)
		}
		for leaf, value := range params {
			if gotParams[leaf] != value {
				t.Errorf("%s%s = %q, want %q", path, leaf, gotParams[leaf], value)
			}
		}
	}
}

func TestBuildUSPRequestAddObject(t *testing.T) {
	job := mustJob(t, jobs.TypeAddObject, jobs.AddObjectPayload{
		ObjectPath: "Device.WiFi.SSID.",
		Parameters: []jobs.ParameterWrite{
			{Name: "SSID", Value: "guest"},
			{Name: "Enable", Value: "true"},
		},
	})
	wire, err := buildUSPRequest("m-3", job)
	if err != nil {
		t.Fatalf("buildUSPRequest: %v", err)
	}
	msg, err := usp.DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	add := msg.GetBody().GetRequest().GetAdd()
	if add == nil {
		t.Fatal("not an Add message")
	}
	if !add.GetAllowPartial() {
		t.Error("allow_partial = false, want true")
	}
	createObjs := add.GetCreateObjs()
	if len(createObjs) != 1 || createObjs[0].GetObjPath() != "Device.WiFi.SSID." {
		t.Fatalf("create_objs = %+v", createObjs)
	}
	params := map[string]string{}
	for _, s := range createObjs[0].GetParamSettings() {
		params[s.GetParam()] = s.GetValue()
	}
	if params["SSID"] != "guest" || params["Enable"] != "true" {
		t.Errorf("param_settings = %+v", params)
	}
}

func TestBuildUSPRequestAddObjectNoParams(t *testing.T) {
	job := mustJob(t, jobs.TypeAddObject, jobs.AddObjectPayload{
		ObjectPath: "Device.WiFi.SSID.",
	})
	wire, err := buildUSPRequest("m-4", job)
	if err != nil {
		t.Fatalf("buildUSPRequest: %v", err)
	}
	msg, err := usp.DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	add := msg.GetBody().GetRequest().GetAdd()
	if add == nil {
		t.Fatal("not an Add message")
	}
	createObjs := add.GetCreateObjs()
	if len(createObjs) != 1 || createObjs[0].GetObjPath() != "Device.WiFi.SSID." {
		t.Fatalf("create_objs = %+v", createObjs)
	}
	if got := createObjs[0].GetParamSettings(); len(got) != 0 {
		t.Errorf("param_settings = %+v, want empty", got)
	}
}

func TestBuildUSPRequestDeleteObject(t *testing.T) {
	job := mustJob(t, jobs.TypeDeleteObject, jobs.DeleteObjectPayload{
		ObjectPath: "Device.WiFi.SSID.3.",
	})
	wire, err := buildUSPRequest("m-5", job)
	if err != nil {
		t.Fatalf("buildUSPRequest: %v", err)
	}
	msg, err := usp.DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	del := msg.GetBody().GetRequest().GetDelete()
	if del == nil {
		t.Fatal("not a Delete message")
	}
	if !del.GetAllowPartial() {
		t.Error("allow_partial = false, want true")
	}
	if paths := del.GetObjPaths(); len(paths) != 1 || paths[0] != "Device.WiFi.SSID.3." {
		t.Errorf("obj_paths = %v", paths)
	}
}

func TestBuildUSPRequestReboot(t *testing.T) {
	job := mustJob(t, jobs.TypeReboot, jobs.RebootPayload{})
	job.CommandKey = "reboot-ck-1"
	wire, err := buildUSPRequest("m-6", job)
	if err != nil {
		t.Fatalf("buildUSPRequest: %v", err)
	}
	msg, err := usp.DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	op := msg.GetBody().GetRequest().GetOperate()
	if op == nil {
		t.Fatal("not an Operate message")
	}
	if op.GetCommand() != "Device.Reboot()" {
		t.Errorf("command = %q, want Device.Reboot()", op.GetCommand())
	}
	if op.GetCommandKey() != "reboot-ck-1" {
		t.Errorf("command_key = %q, want reboot-ck-1", op.GetCommandKey())
	}
	if !op.GetSendResp() {
		t.Error("send_resp = false, want true")
	}
	if got := op.GetInputArgs()["Cause"]; got != "RemoteReboot" {
		t.Errorf("Cause = %q, want RemoteReboot", got)
	}
}

func TestBuildUSPRequestFactoryReset(t *testing.T) {
	job := mustJob(t, jobs.TypeFactoryReset, jobs.FactoryResetPayload{})
	wire, err := buildUSPRequest("m-7", job)
	if err != nil {
		t.Fatalf("buildUSPRequest: %v", err)
	}
	msg, err := usp.DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	op := msg.GetBody().GetRequest().GetOperate()
	if op == nil {
		t.Fatal("not an Operate message")
	}
	if op.GetCommand() != "Device.FactoryReset()" {
		t.Errorf("command = %q, want Device.FactoryReset()", op.GetCommand())
	}
	if got := op.GetInputArgs()["Cause"]; got != "RemoteFactoryReset" {
		t.Errorf("Cause = %q, want RemoteFactoryReset", got)
	}
}

func TestBuildUSPRequestDiagnosticsPing(t *testing.T) {
	job := mustJob(t, jobs.TypeDiagnosticsPing, jobs.DiagnosticsPingPayload{
		Host:                "8.8.8.8",
		NumberOfRepetitions: 4,
		Timeout:             5000,
		DataBlockSize:       64,
		DSCP:                0,
	})
	wire, err := buildUSPRequest("m-8", job)
	if err != nil {
		t.Fatalf("buildUSPRequest: %v", err)
	}
	msg, err := usp.DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	op := msg.GetBody().GetRequest().GetOperate()
	if op == nil {
		t.Fatal("not an Operate message")
	}
	if op.GetCommand() != "Device.IP.Diagnostics.IPPing()" {
		t.Errorf("command = %q, want Device.IP.Diagnostics.IPPing()", op.GetCommand())
	}
	args := op.GetInputArgs()
	want := map[string]string{
		"ProtocolVersion":     "Any",
		"Host":                "8.8.8.8",
		"NumberOfRepetitions": "4",
		"Timeout":             "5000",
		"DataBlockSize":       "64",
		"DSCP":                "0",
	}
	for k, v := range want {
		if args[k] != v {
			t.Errorf("input_args[%q] = %q, want %q", k, args[k], v)
		}
	}
}

func TestBuildUSPRequestDiagnosticsTraceroute(t *testing.T) {
	job := mustJob(t, jobs.TypeDiagnosticsTraceroute, jobs.DiagnosticsTraceroutePayload{
		Host:          "8.8.8.8",
		NumberOfTries: 3,
		Timeout:       5000,
		DataBlockSize: 38,
		DSCP:          0,
		MaxHopCount:   30,
	})
	wire, err := buildUSPRequest("m-9", job)
	if err != nil {
		t.Fatalf("buildUSPRequest: %v", err)
	}
	msg, err := usp.DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	op := msg.GetBody().GetRequest().GetOperate()
	if op == nil {
		t.Fatal("not an Operate message")
	}
	if op.GetCommand() != "Device.IP.Diagnostics.TraceRoute()" {
		t.Errorf("command = %q, want Device.IP.Diagnostics.TraceRoute()", op.GetCommand())
	}
	args := op.GetInputArgs()
	want := map[string]string{
		"ProtocolVersion": "Any",
		"Host":            "8.8.8.8",
		"NumberOfTries":   "3",
		"Timeout":         "5000",
		"DataBlockSize":   "38",
		"DSCP":            "0",
		"MaxHopCount":     "30",
	}
	for k, v := range want {
		if args[k] != v {
			t.Errorf("input_args[%q] = %q, want %q", k, args[k], v)
		}
	}
}

func TestBuildUSPRequestParameterDiscovery(t *testing.T) {
	job := mustJob(t, jobs.TypeParameterDiscovery, jobs.ParameterDiscoveryPayload{
		Root:         "Device.",
		FallbackRoot: "InternetGatewayDevice.",
	})
	wire, err := buildUSPRequest("m-10", job)
	if err != nil {
		t.Fatalf("buildUSPRequest: %v", err)
	}
	msg, err := usp.DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	dm := msg.GetBody().GetRequest().GetGetSupportedDm()
	if dm == nil {
		t.Fatal("not a GetSupportedDM message")
	}
	if got := dm.GetObjPaths(); len(got) != 1 || got[0] != "Device." {
		t.Errorf("obj_paths = %v, want [Device.]", got)
	}
	if dm.GetFirstLevelOnly() {
		t.Error("first_level_only = true, want false")
	}
	if !dm.GetReturnCommands() || !dm.GetReturnEvents() || !dm.GetReturnParams() {
		t.Errorf("return flags = %v/%v/%v, want all true", dm.GetReturnCommands(), dm.GetReturnEvents(), dm.GetReturnParams())
	}
}

func TestBuildUSPRequestParameterDiscoveryUsesFallbackRootLiterally(t *testing.T) {
	// buildUSPRequest only ever builds from payload.Root -- it must not
	// substitute FallbackRoot even when set; the fallback retry is the
	// dispatcher's concern (Task 4), not this function's.
	job := mustJob(t, jobs.TypeParameterDiscovery, jobs.ParameterDiscoveryPayload{
		Root:         "InternetGatewayDevice.",
		FallbackRoot: "Device.",
		IsFallback:   true,
	})
	wire, err := buildUSPRequest("m-11", job)
	if err != nil {
		t.Fatalf("buildUSPRequest: %v", err)
	}
	msg, err := usp.DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	dm := msg.GetBody().GetRequest().GetGetSupportedDm()
	if got := dm.GetObjPaths(); len(got) != 1 || got[0] != "InternetGatewayDevice." {
		t.Errorf("obj_paths = %v, want [InternetGatewayDevice.]", got)
	}
}

func TestBuildUSPRequestUnsupportedType(t *testing.T) {
	job := mustJob(t, jobs.TypeFirmwareDownload, jobs.FirmwareDownloadPayload{
		FirmwareImageID: "fw-1",
		URL:             "https://example.test/fw.bin",
		FileSize:        12345,
	})
	_, err := buildUSPRequest("m-12", job)
	if !errors.Is(err, ErrUnsupportedOverUSP) {
		t.Fatalf("err = %v, want ErrUnsupportedOverUSP", err)
	}
}

func TestBuildUSPRequestUnknownType(t *testing.T) {
	for _, jobType := range []string{
		jobs.TypeConnectionRequest,
		jobs.TypeScheduleInform,
		jobs.TypeSetParameterAttributes,
		jobs.TypeGetParameterAttributes,
		jobs.TypeUpload,
		"NOT_A_REAL_JOB_TYPE",
	} {
		job := mustJob(t, jobType, struct{}{})
		_, err := buildUSPRequest("m-13", job)
		if !errors.Is(err, ErrUnsupportedOverUSP) {
			t.Errorf("job type %s: err = %v, want ErrUnsupportedOverUSP", jobType, err)
		}
	}
}

func TestGroupParametersByObjectPath(t *testing.T) {
	tests := []struct {
		name   string
		params []jobs.ParameterWrite
		want   map[string]map[string]string
	}{
		{
			name: "single param",
			params: []jobs.ParameterWrite{
				{Name: "Device.WiFi.SSID.1.SSID", Value: "acs-test"},
			},
			want: map[string]map[string]string{
				"Device.WiFi.SSID.1.": {"SSID": "acs-test"},
			},
		},
		{
			name: "multiple params on one object",
			params: []jobs.ParameterWrite{
				{Name: "Device.WiFi.SSID.1.SSID", Value: "acs-test"},
				{Name: "Device.WiFi.SSID.1.Enable", Value: "true"},
			},
			want: map[string]map[string]string{
				"Device.WiFi.SSID.1.": {"SSID": "acs-test", "Enable": "true"},
			},
		},
		{
			name: "params on two different objects",
			params: []jobs.ParameterWrite{
				{Name: "Device.WiFi.SSID.1.SSID", Value: "acs-test"},
				{Name: "Device.WiFi.SSID.2.SSID", Value: "guest"},
			},
			want: map[string]map[string]string{
				"Device.WiFi.SSID.1.": {"SSID": "acs-test"},
				"Device.WiFi.SSID.2.": {"SSID": "guest"},
			},
		},
		{
			// A dot-less name has no real TR-181 precedent (every
			// parameter lives at least two segments below the root),
			// but groupParametersByObjectPath must not panic on it: it
			// groups under the empty-string object path with the whole
			// name as the leaf.
			name: "path with no dot",
			params: []jobs.ParameterWrite{
				{Name: "SSID", Value: "acs-test"},
			},
			want: map[string]map[string]string{
				"": {"SSID": "acs-test"},
			},
		},
		{
			name:   "empty input",
			params: nil,
			want:   map[string]map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := groupParametersByObjectPath(tt.params)
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for path, params := range tt.want {
				gotParams, ok := got[path]
				if !ok {
					t.Fatalf("missing object path %q in %+v", path, got)
				}
				if len(gotParams) != len(params) {
					t.Fatalf("object %q params = %+v, want %+v", path, gotParams, params)
				}
				for leaf, value := range params {
					if gotParams[leaf] != value {
						t.Errorf("%s%s = %q, want %q", path, leaf, gotParams[leaf], value)
					}
				}
			}
		})
	}
}
