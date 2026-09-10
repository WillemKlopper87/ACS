package usp

import (
	"errors"
	"testing"

	"acs/internal/usp/uspproto"
)

func TestNewMsgIDIsUnique(t *testing.T) {
	a, b := NewMsgID(), NewMsgID()
	if a == "" || b == "" {
		t.Fatal("NewMsgID returned an empty id")
	}
	if a == b {
		t.Errorf("NewMsgID returned the same id twice: %q", a)
	}
}

func TestEncodeGet(t *testing.T) {
	wire, err := EncodeGet("m-1", []string{"Device.DeviceInfo.", "Device.WiFi."}, 2)
	if err != nil {
		t.Fatalf("EncodeGet: %v", err)
	}
	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	if msg.GetHeader().GetMsgId() != "m-1" {
		t.Errorf("msg_id = %q, want m-1", msg.GetHeader().GetMsgId())
	}
	if msg.GetHeader().GetMsgType() != uspproto.Header_GET {
		t.Errorf("msg_type = %v, want GET", msg.GetHeader().GetMsgType())
	}
	get := msg.GetBody().GetRequest().GetGet()
	if len(get.GetParamPaths()) != 2 || get.GetParamPaths()[0] != "Device.DeviceInfo." {
		t.Errorf("param_paths = %v", get.GetParamPaths())
	}
	if get.GetMaxDepth() != 2 {
		t.Errorf("max_depth = %d, want 2", get.GetMaxDepth())
	}
}

func TestEncodeSet(t *testing.T) {
	wire, err := EncodeSet("m-2", false, map[string]map[string]string{
		"Device.WiFi.SSID.1.": {"SSID": "acs-test"},
	})
	if err != nil {
		t.Fatalf("EncodeSet: %v", err)
	}
	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}
	if msg.GetHeader().GetMsgType() != uspproto.Header_SET {
		t.Errorf("msg_type = %v, want SET", msg.GetHeader().GetMsgType())
	}
	set := msg.GetBody().GetRequest().GetSet()
	if set.GetAllowPartial() {
		t.Error("allow_partial = true, want false")
	}
	objs := set.GetUpdateObjs()
	if len(objs) != 1 || objs[0].GetObjPath() != "Device.WiFi.SSID.1." {
		t.Fatalf("update_objs = %+v", objs)
	}
	params := objs[0].GetParamSettings()
	if len(params) != 1 || params[0].GetParam() != "SSID" || params[0].GetValue() != "acs-test" {
		t.Errorf("param_settings = %+v", params)
	}
}

func TestEncodeAddDeleteOperate(t *testing.T) {
	addWire, err := EncodeAdd("m-3", true, "Device.WiFi.SSID.", map[string]string{"SSID": "guest"})
	if err != nil {
		t.Fatalf("EncodeAdd: %v", err)
	}
	addMsg, err := DecodeMsg(addWire)
	if err != nil {
		t.Fatal(err)
	}
	if addMsg.GetHeader().GetMsgType() != uspproto.Header_ADD {
		t.Errorf("add msg_type = %v", addMsg.GetHeader().GetMsgType())
	}
	if !addMsg.GetBody().GetRequest().GetAdd().GetAllowPartial() {
		t.Error("add allow_partial = false, want true")
	}

	delWire, err := EncodeDelete("m-4", false, []string{"Device.WiFi.SSID.2."})
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	delMsg, err := DecodeMsg(delWire)
	if err != nil {
		t.Fatal(err)
	}
	if delMsg.GetHeader().GetMsgType() != uspproto.Header_DELETE {
		t.Errorf("delete msg_type = %v", delMsg.GetHeader().GetMsgType())
	}
	if paths := delMsg.GetBody().GetRequest().GetDelete().GetObjPaths(); len(paths) != 1 || paths[0] != "Device.WiFi.SSID.2." {
		t.Errorf("delete obj_paths = %v", paths)
	}

	// command_key is the field OperationComplete echoes back, and it is
	// how an async operation correlates to a job. It must survive
	// encoding exactly as given.
	opWire, err := EncodeOperate("m-5", "Device.Reboot()", "job-abc-123", true, nil)
	if err != nil {
		t.Fatalf("EncodeOperate: %v", err)
	}
	opMsg, err := DecodeMsg(opWire)
	if err != nil {
		t.Fatal(err)
	}
	op := opMsg.GetBody().GetRequest().GetOperate()
	if op.GetCommand() != "Device.Reboot()" {
		t.Errorf("command = %q", op.GetCommand())
	}
	if op.GetCommandKey() != "job-abc-123" {
		t.Errorf("command_key = %q, want job-abc-123 -- async job correlation depends on this", op.GetCommandKey())
	}
	if !op.GetSendResp() {
		t.Error("send_resp = false, want true")
	}
}

func TestEncodeDiscoveryMessages(t *testing.T) {
	dmWire, err := EncodeGetSupportedDM("m-6", []string{"Device."}, false, true, true, true)
	if err != nil {
		t.Fatalf("EncodeGetSupportedDM: %v", err)
	}
	dmMsg, err := DecodeMsg(dmWire)
	if err != nil {
		t.Fatal(err)
	}
	if dmMsg.GetHeader().GetMsgType() != uspproto.Header_GET_SUPPORTED_DM {
		t.Errorf("msg_type = %v", dmMsg.GetHeader().GetMsgType())
	}
	dm := dmMsg.GetBody().GetRequest().GetGetSupportedDm()
	if dm.GetFirstLevelOnly() || !dm.GetReturnCommands() || !dm.GetReturnEvents() || !dm.GetReturnParams() {
		t.Errorf("flags = %+v", dm)
	}

	instWire, err := EncodeGetInstances("m-7", []string{"Device.WiFi.SSID."}, true)
	if err != nil {
		t.Fatalf("EncodeGetInstances: %v", err)
	}
	instMsg, err := DecodeMsg(instWire)
	if err != nil {
		t.Fatal(err)
	}
	if instMsg.GetHeader().GetMsgType() != uspproto.Header_GET_INSTANCES {
		t.Errorf("msg_type = %v", instMsg.GetHeader().GetMsgType())
	}
	if !instMsg.GetBody().GetRequest().GetGetInstances().GetFirstLevelOnly() {
		t.Error("first_level_only = false, want true")
	}

	protoWire, err := EncodeGetSupportedProtocol("m-8", "1.0,1.1,1.2,1.3")
	if err != nil {
		t.Fatalf("EncodeGetSupportedProtocol: %v", err)
	}
	protoMsg, err := DecodeMsg(protoWire)
	if err != nil {
		t.Fatal(err)
	}
	if protoMsg.GetHeader().GetMsgType() != uspproto.Header_GET_SUPPORTED_PROTO {
		t.Errorf("msg_type = %v", protoMsg.GetHeader().GetMsgType())
	}
	if got := protoMsg.GetBody().GetRequest().GetGetSupportedProtocol().GetControllerSupportedProtocolVersions(); got != "1.0,1.1,1.2,1.3" {
		t.Errorf("controller_supported_protocol_versions = %q", got)
	}
}

func TestDecodeMsgGarbage(t *testing.T) {
	if _, err := DecodeMsg([]byte{0xff, 0xff, 0xff, 0xff}); !errors.Is(err, ErrMalformedMessage) {
		t.Errorf("got %v, want ErrMalformedMessage", err)
	}
}
