package usp

import (
	"bytes"
	"errors"
	"testing"

	"acs/internal/usp/uspproto"

	"google.golang.org/protobuf/proto"
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

// A field-less protobuf is valid wire and unmarshals with nil Header and
// nil Body -- a distinct path from unparseable bytes, and one a truncated
// or attacker-crafted record can reach. It must be ErrMalformedMessage too.
func TestDecodeMsgEmptyIsMalformed(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeMsg(wire); !errors.Is(err, ErrMalformedMessage) {
		t.Errorf("empty Msg gave %v, want ErrMalformedMessage", err)
	}
	// Header present but Body absent must also be refused.
	wire, err = proto.Marshal(&uspproto.Msg{Header: &uspproto.Header{MsgId: "m-9", MsgType: uspproto.Header_GET}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeMsg(wire); !errors.Is(err, ErrMalformedMessage) {
		t.Errorf("Msg with nil Body gave %v, want ErrMalformedMessage", err)
	}
}

// EncodeSet and EncodeAdd iterate maps; without sorting the parameter
// and object-path keys before encoding, identical input can produce
// different wire bytes from run to run. Two objects and two parameters
// per object give map iteration enough entries that this would actually
// flip byte order on an unsorted implementation, rather than passing by
// accident with a single-entry map.
func TestEncodeSetAddDeterministic(t *testing.T) {
	updates := map[string]map[string]string{
		"Device.WiFi.SSID.1.": {"SSID": "acs-test", "Enable": "true"},
		"Device.WiFi.SSID.2.": {"SSID": "acs-guest", "Enable": "false"},
	}
	first, err := EncodeSet("m-det-1", false, updates)
	if err != nil {
		t.Fatalf("EncodeSet: %v", err)
	}
	for i := 0; i < 10; i++ {
		again, err := EncodeSet("m-det-1", false, updates)
		if err != nil {
			t.Fatalf("EncodeSet: %v", err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("EncodeSet produced different bytes across runs on iteration %d: non-deterministic map ordering", i)
		}
	}

	params := map[string]string{"SSID": "acs-test", "Enable": "true"}
	firstAdd, err := EncodeAdd("m-det-2", false, "Device.WiFi.SSID.", params)
	if err != nil {
		t.Fatalf("EncodeAdd: %v", err)
	}
	for i := 0; i < 10; i++ {
		again, err := EncodeAdd("m-det-2", false, "Device.WiFi.SSID.", params)
		if err != nil {
			t.Fatalf("EncodeAdd: %v", err)
		}
		if !bytes.Equal(firstAdd, again) {
			t.Fatalf("EncodeAdd produced different bytes across runs on iteration %d: non-deterministic map ordering", i)
		}
	}
}

func TestDecodeOnBoardRequest(t *testing.T) {
	// Build a Notify with OnBoardRequest notification.
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-onboard-1",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-abc-123",
							SendResp:       true,
							Notification: &uspproto.Notify_OnBoardReq{
								OnBoardReq: &uspproto.Notify_OnBoardRequest{
									Oui:                            "0025C2",
									ProductClass:                   "Gateway",
									SerialNumber:                   "SN12345",
									AgentSupportedProtocolVersions: "1.0,1.1,1.2,1.3",
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	obr, err := DecodeOnBoardRequest(msg)
	if err != nil {
		t.Fatalf("DecodeOnBoardRequest: %v", err)
	}

	if obr.SubscriptionID != "sub-abc-123" {
		t.Errorf("SubscriptionID = %q, want sub-abc-123", obr.SubscriptionID)
	}
	if !obr.SendResp {
		t.Error("SendResp = false, want true")
	}
	if obr.OUI != "0025C2" {
		t.Errorf("OUI = %q, want 0025C2", obr.OUI)
	}
	if obr.ProductClass != "Gateway" {
		t.Errorf("ProductClass = %q, want Gateway", obr.ProductClass)
	}
	if obr.SerialNumber != "SN12345" {
		t.Errorf("SerialNumber = %q, want SN12345", obr.SerialNumber)
	}
	if obr.AgentSupportedProtocolVersions != "1.0,1.1,1.2,1.3" {
		t.Errorf("AgentSupportedProtocolVersions = %q, want 1.0,1.1,1.2,1.3", obr.AgentSupportedProtocolVersions)
	}
}

func TestDecodeOnBoardRequestWrongVariant(t *testing.T) {
	// Build a Notify with ValueChange (not OnBoardRequest).
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-valuechange-1",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-xyz-789",
							SendResp:       false,
							Notification: &uspproto.Notify_ValueChange_{
								ValueChange: &uspproto.Notify_ValueChange{
									ParamPath:  "Device.SomeParam",
									ParamValue: "new-value",
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	_, err = DecodeOnBoardRequest(msg)
	if !errors.Is(err, ErrNotOnBoardRequest) {
		t.Errorf("DecodeOnBoardRequest returned %v, want ErrNotOnBoardRequest", err)
	}
}

func TestDecodeOnBoardRequestWrongMsgType(t *testing.T) {
	// Build a GetResp (not a Notify).
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-getresp-1",
			MsgType: uspproto.Header_GET_RESP,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Response{
				Response: &uspproto.Response{
					RespType: &uspproto.Response_GetResp{
						GetResp: &uspproto.GetResp{},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	_, err = DecodeOnBoardRequest(msg)
	if !errors.Is(err, ErrNotOnBoardRequest) {
		t.Errorf("DecodeOnBoardRequest returned %v, want ErrNotOnBoardRequest", err)
	}
}

func TestDecodeOperationComplete(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-opercomplete-1",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-oc-1",
							SendResp:       true,
							Notification: &uspproto.Notify_OperComplete{
								OperComplete: &uspproto.Notify_OperationComplete{
									ObjPath:     "Device.",
									CommandName: "Device.Reboot()",
									CommandKey:  "job-abc-123",
									OperationResp: &uspproto.Notify_OperationComplete_ReqOutputArgs{
										ReqOutputArgs: &uspproto.Notify_OperationComplete_OutputArgs{
											OutputArgs: map[string]string{"Status": "Complete"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	oc, err := DecodeOperationComplete(msg)
	if err != nil {
		t.Fatalf("DecodeOperationComplete: %v", err)
	}

	if oc.SubscriptionID != "sub-oc-1" {
		t.Errorf("SubscriptionID = %q, want sub-oc-1", oc.SubscriptionID)
	}
	if !oc.SendResp {
		t.Error("SendResp = false, want true")
	}
	if oc.ObjPath != "Device." {
		t.Errorf("ObjPath = %q, want Device.", oc.ObjPath)
	}
	if oc.CommandName != "Device.Reboot()" {
		t.Errorf("CommandName = %q, want Device.Reboot()", oc.CommandName)
	}
	if oc.CommandKey != "job-abc-123" {
		t.Errorf("CommandKey = %q, want job-abc-123", oc.CommandKey)
	}
	if oc.Failed {
		t.Error("Failed = true, want false")
	}
	if len(oc.OutputArgs) != 1 || oc.OutputArgs["Status"] != "Complete" {
		t.Errorf("OutputArgs = %+v, want {Status: Complete}", oc.OutputArgs)
	}
	if oc.ErrCode != 0 || oc.ErrMsg != "" {
		t.Errorf("ErrCode/ErrMsg = %d/%q, want zero values for a successful operation", oc.ErrCode, oc.ErrMsg)
	}
}

func TestDecodeOperationCompleteWithError(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-opercomplete-2",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-oc-2",
							SendResp:       false,
							Notification: &uspproto.Notify_OperComplete{
								OperComplete: &uspproto.Notify_OperationComplete{
									ObjPath:     "Device.",
									CommandName: "Device.Reboot()",
									CommandKey:  "job-def-456",
									OperationResp: &uspproto.Notify_OperationComplete_CmdFailure{
										CmdFailure: &uspproto.Notify_OperationComplete_CommandFailure{
											ErrCode: 7012,
											ErrMsg:  "command failed on device",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	oc, err := DecodeOperationComplete(msg)
	if err != nil {
		t.Fatalf("DecodeOperationComplete: %v", err)
	}

	if !oc.Failed {
		t.Fatal("Failed = false, want true")
	}
	if oc.ErrCode != 7012 {
		t.Errorf("ErrCode = %d, want 7012", oc.ErrCode)
	}
	if oc.ErrMsg != "command failed on device" {
		t.Errorf("ErrMsg = %q, want %q", oc.ErrMsg, "command failed on device")
	}
	if oc.OutputArgs != nil {
		t.Errorf("OutputArgs = %+v, want nil for a failed operation", oc.OutputArgs)
	}
	if oc.CommandKey != "job-def-456" {
		t.Errorf("CommandKey = %q, want job-def-456", oc.CommandKey)
	}
}

// TestDecodeOperationCompleteNilOperationResp covers the case protobuf
// itself allows even though the spec doesn't intend it: an
// OperationComplete whose OperationResp oneof is left entirely unset. It
// must not panic, and must decode the same as a successful operation
// with no output arguments -- Failed false, OutputArgs nil/empty.
func TestDecodeOperationCompleteNilOperationResp(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-opercomplete-3",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-oc-3",
							SendResp:       false,
							Notification: &uspproto.Notify_OperComplete{
								OperComplete: &uspproto.Notify_OperationComplete{
									ObjPath:     "Device.",
									CommandName: "Device.Reboot()",
									CommandKey:  "job-ghi-789",
									// OperationResp deliberately left unset.
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	oc, err := DecodeOperationComplete(msg)
	if err != nil {
		t.Fatalf("DecodeOperationComplete: %v (must not panic or error on an unset OperationResp oneof)", err)
	}
	if oc.Failed {
		t.Error("Failed = true, want false for an unset OperationResp")
	}
	if oc.OutputArgs != nil {
		t.Errorf("OutputArgs = %+v, want nil for an unset OperationResp", oc.OutputArgs)
	}
}

func TestDecodeOperationCompleteWrongVariant(t *testing.T) {
	// Build a Notify with OnBoardRequest (not OperComplete).
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-onboard-wrong-variant",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-abc-123",
							SendResp:       true,
							Notification: &uspproto.Notify_OnBoardReq{
								OnBoardReq: &uspproto.Notify_OnBoardRequest{
									Oui:          "0025C2",
									ProductClass: "Gateway",
									SerialNumber: "SN12345",
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	_, err = DecodeOperationComplete(msg)
	if !errors.Is(err, ErrNotOperationComplete) {
		t.Errorf("DecodeOperationComplete returned %v, want ErrNotOperationComplete", err)
	}
}

func TestDecodeOperationCompleteWrongMsgType(t *testing.T) {
	// Build a GetResp (not a Notify).
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-getresp-2",
			MsgType: uspproto.Header_GET_RESP,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Response{
				Response: &uspproto.Response{
					RespType: &uspproto.Response_GetResp{
						GetResp: &uspproto.GetResp{},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	_, err = DecodeOperationComplete(msg)
	if !errors.Is(err, ErrNotOperationComplete) {
		t.Errorf("DecodeOperationComplete returned %v, want ErrNotOperationComplete", err)
	}
}

func TestEncodeNotifyRespRoundTrip(t *testing.T) {
	wire, err := EncodeNotifyResp("m-notify-resp-1", "sub-response-123")
	if err != nil {
		t.Fatalf("EncodeNotifyResp: %v", err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	if msg.GetHeader().GetMsgType() != uspproto.Header_NOTIFY_RESP {
		t.Errorf("msg_type = %v, want NOTIFY_RESP", msg.GetHeader().GetMsgType())
	}

	notifyResp := msg.GetBody().GetResponse().GetNotifyResp()
	if notifyResp == nil {
		t.Fatal("NotifyResp is nil")
	}
	if notifyResp.GetSubscriptionId() != "sub-response-123" {
		t.Errorf("subscription_id = %q, want sub-response-123", notifyResp.GetSubscriptionId())
	}
}

func TestDecodeValueChange(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-valuechange-1",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-vc-1",
							SendResp:       true,
							Notification: &uspproto.Notify_ValueChange_{
								ValueChange: &uspproto.Notify_ValueChange{
									ParamPath:  "Device.SomeParam",
									ParamValue: "new-value",
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	vc, err := DecodeValueChange(msg)
	if err != nil {
		t.Fatalf("DecodeValueChange: %v", err)
	}

	if vc.SubscriptionID != "sub-vc-1" {
		t.Errorf("SubscriptionID = %q, want sub-vc-1", vc.SubscriptionID)
	}
	if !vc.SendResp {
		t.Error("SendResp = false, want true")
	}
	if vc.ParamPath != "Device.SomeParam" {
		t.Errorf("ParamPath = %q, want Device.SomeParam", vc.ParamPath)
	}
	if vc.ParamValue != "new-value" {
		t.Errorf("ParamValue = %q, want new-value", vc.ParamValue)
	}
}

func TestDecodeValueChangeWrongVariant(t *testing.T) {
	t.Run("OnBoardRequest", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-vc-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-vc-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_OnBoardReq{
									OnBoardReq: &uspproto.Notify_OnBoardRequest{
										Oui: "0025C2",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeValueChange(msg)
		if !errors.Is(err, ErrNotValueChange) {
			t.Errorf("DecodeValueChange returned %v, want ErrNotValueChange", err)
		}
	})

	t.Run("OperComplete", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-vc-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-vc-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_OperComplete{
									OperComplete: &uspproto.Notify_OperationComplete{
										ObjPath: "Device.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeValueChange(msg)
		if !errors.Is(err, ErrNotValueChange) {
			t.Errorf("DecodeValueChange returned %v, want ErrNotValueChange", err)
		}
	})

	t.Run("ObjectCreation", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-vc-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-vc-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_ObjCreation{
									ObjCreation: &uspproto.Notify_ObjectCreation{
										ObjPath: "Device.WiFi.SSID.1.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeValueChange(msg)
		if !errors.Is(err, ErrNotValueChange) {
			t.Errorf("DecodeValueChange returned %v, want ErrNotValueChange", err)
		}
	})

	t.Run("ObjectDeletion", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-vc-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-vc-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_ObjDeletion{
									ObjDeletion: &uspproto.Notify_ObjectDeletion{
										ObjPath: "Device.WiFi.SSID.1.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeValueChange(msg)
		if !errors.Is(err, ErrNotValueChange) {
			t.Errorf("DecodeValueChange returned %v, want ErrNotValueChange", err)
		}
	})

	t.Run("Event", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-vc-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-vc-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_Event_{
									Event: &uspproto.Notify_Event{
										ObjPath: "Device.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeValueChange(msg)
		if !errors.Is(err, ErrNotValueChange) {
			t.Errorf("DecodeValueChange returned %v, want ErrNotValueChange", err)
		}
	})
}

func TestDecodeValueChangeWrongMsgType(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-vc-getresp",
			MsgType: uspproto.Header_GET_RESP,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Response{
				Response: &uspproto.Response{
					RespType: &uspproto.Response_GetResp{
						GetResp: &uspproto.GetResp{},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	_, err = DecodeValueChange(msg)
	if !errors.Is(err, ErrNotValueChange) {
		t.Errorf("DecodeValueChange returned %v, want ErrNotValueChange", err)
	}
}

func TestDecodeValueChangeNilPayload(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-vc-nil",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-vc-nil",
							SendResp:       false,
							Notification: &uspproto.Notify_ValueChange_{
								ValueChange: nil,
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	vc, err := DecodeValueChange(msg)
	if err != nil {
		t.Fatalf("DecodeValueChange: %v (must not panic or error on nil payload)", err)
	}
	if vc.ParamPath != "" {
		t.Errorf("ParamPath = %q, want empty string for nil payload", vc.ParamPath)
	}
	if vc.ParamValue != "" {
		t.Errorf("ParamValue = %q, want empty string for nil payload", vc.ParamValue)
	}
}

func TestDecodeObjectCreation(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-objcreation-1",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-oc-1",
							SendResp:       false,
							Notification: &uspproto.Notify_ObjCreation{
								ObjCreation: &uspproto.Notify_ObjectCreation{
									ObjPath: "Device.WiFi.SSID.1.",
									UniqueKeys: map[string]string{
										"Alias": "MySSID",
									},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	oc, err := DecodeObjectCreation(msg)
	if err != nil {
		t.Fatalf("DecodeObjectCreation: %v", err)
	}

	if oc.SubscriptionID != "sub-oc-1" {
		t.Errorf("SubscriptionID = %q, want sub-oc-1", oc.SubscriptionID)
	}
	if oc.SendResp {
		t.Error("SendResp = true, want false")
	}
	if oc.ObjPath != "Device.WiFi.SSID.1." {
		t.Errorf("ObjPath = %q, want Device.WiFi.SSID.1.", oc.ObjPath)
	}
	if len(oc.UniqueKeys) != 1 || oc.UniqueKeys["Alias"] != "MySSID" {
		t.Errorf("UniqueKeys = %+v, want {Alias: MySSID}", oc.UniqueKeys)
	}
}

func TestDecodeObjectCreationWrongVariant(t *testing.T) {
	t.Run("OnBoardRequest", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-oc-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-oc-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_OnBoardReq{
									OnBoardReq: &uspproto.Notify_OnBoardRequest{
										Oui: "0025C2",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeObjectCreation(msg)
		if !errors.Is(err, ErrNotObjectCreation) {
			t.Errorf("DecodeObjectCreation returned %v, want ErrNotObjectCreation", err)
		}
	})

	t.Run("OperComplete", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-oc-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-oc-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_OperComplete{
									OperComplete: &uspproto.Notify_OperationComplete{
										ObjPath: "Device.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeObjectCreation(msg)
		if !errors.Is(err, ErrNotObjectCreation) {
			t.Errorf("DecodeObjectCreation returned %v, want ErrNotObjectCreation", err)
		}
	})

	t.Run("ValueChange", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-oc-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-oc-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_ValueChange_{
									ValueChange: &uspproto.Notify_ValueChange{
										ParamPath: "Device.Param",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeObjectCreation(msg)
		if !errors.Is(err, ErrNotObjectCreation) {
			t.Errorf("DecodeObjectCreation returned %v, want ErrNotObjectCreation", err)
		}
	})

	t.Run("ObjectDeletion", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-oc-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-oc-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_ObjDeletion{
									ObjDeletion: &uspproto.Notify_ObjectDeletion{
										ObjPath: "Device.WiFi.SSID.1.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeObjectCreation(msg)
		if !errors.Is(err, ErrNotObjectCreation) {
			t.Errorf("DecodeObjectCreation returned %v, want ErrNotObjectCreation", err)
		}
	})

	t.Run("Event", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-oc-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-oc-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_Event_{
									Event: &uspproto.Notify_Event{
										ObjPath: "Device.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeObjectCreation(msg)
		if !errors.Is(err, ErrNotObjectCreation) {
			t.Errorf("DecodeObjectCreation returned %v, want ErrNotObjectCreation", err)
		}
	})
}

func TestDecodeObjectCreationWrongMsgType(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-oc-getresp",
			MsgType: uspproto.Header_GET_RESP,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Response{
				Response: &uspproto.Response{
					RespType: &uspproto.Response_GetResp{
						GetResp: &uspproto.GetResp{},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	_, err = DecodeObjectCreation(msg)
	if !errors.Is(err, ErrNotObjectCreation) {
		t.Errorf("DecodeObjectCreation returned %v, want ErrNotObjectCreation", err)
	}
}

func TestDecodeObjectCreationNilPayload(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-oc-nil",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-oc-nil",
							SendResp:       false,
							Notification: &uspproto.Notify_ObjCreation{
								ObjCreation: nil,
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	oc, err := DecodeObjectCreation(msg)
	if err != nil {
		t.Fatalf("DecodeObjectCreation: %v (must not panic or error on nil payload)", err)
	}
	if oc.ObjPath != "" {
		t.Errorf("ObjPath = %q, want empty string for nil payload", oc.ObjPath)
	}
	if oc.UniqueKeys != nil {
		t.Errorf("UniqueKeys = %+v, want nil for nil payload", oc.UniqueKeys)
	}
}

func TestDecodeObjectDeletion(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-objdeletion-1",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-od-1",
							SendResp:       true,
							Notification: &uspproto.Notify_ObjDeletion{
								ObjDeletion: &uspproto.Notify_ObjectDeletion{
									ObjPath: "Device.WiFi.SSID.1.",
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	od, err := DecodeObjectDeletion(msg)
	if err != nil {
		t.Fatalf("DecodeObjectDeletion: %v", err)
	}

	if od.SubscriptionID != "sub-od-1" {
		t.Errorf("SubscriptionID = %q, want sub-od-1", od.SubscriptionID)
	}
	if !od.SendResp {
		t.Error("SendResp = false, want true")
	}
	if od.ObjPath != "Device.WiFi.SSID.1." {
		t.Errorf("ObjPath = %q, want Device.WiFi.SSID.1.", od.ObjPath)
	}
}

func TestDecodeObjectDeletionWrongVariant(t *testing.T) {
	t.Run("OnBoardRequest", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-od-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-od-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_OnBoardReq{
									OnBoardReq: &uspproto.Notify_OnBoardRequest{
										Oui: "0025C2",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeObjectDeletion(msg)
		if !errors.Is(err, ErrNotObjectDeletion) {
			t.Errorf("DecodeObjectDeletion returned %v, want ErrNotObjectDeletion", err)
		}
	})

	t.Run("OperComplete", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-od-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-od-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_OperComplete{
									OperComplete: &uspproto.Notify_OperationComplete{
										ObjPath: "Device.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeObjectDeletion(msg)
		if !errors.Is(err, ErrNotObjectDeletion) {
			t.Errorf("DecodeObjectDeletion returned %v, want ErrNotObjectDeletion", err)
		}
	})

	t.Run("ValueChange", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-od-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-od-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_ValueChange_{
									ValueChange: &uspproto.Notify_ValueChange{
										ParamPath: "Device.Param",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeObjectDeletion(msg)
		if !errors.Is(err, ErrNotObjectDeletion) {
			t.Errorf("DecodeObjectDeletion returned %v, want ErrNotObjectDeletion", err)
		}
	})

	t.Run("ObjectCreation", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-od-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-od-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_ObjCreation{
									ObjCreation: &uspproto.Notify_ObjectCreation{
										ObjPath: "Device.WiFi.SSID.1.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeObjectDeletion(msg)
		if !errors.Is(err, ErrNotObjectDeletion) {
			t.Errorf("DecodeObjectDeletion returned %v, want ErrNotObjectDeletion", err)
		}
	})

	t.Run("Event", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-od-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-od-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_Event_{
									Event: &uspproto.Notify_Event{
										ObjPath: "Device.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeObjectDeletion(msg)
		if !errors.Is(err, ErrNotObjectDeletion) {
			t.Errorf("DecodeObjectDeletion returned %v, want ErrNotObjectDeletion", err)
		}
	})
}

func TestDecodeObjectDeletionWrongMsgType(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-od-getresp",
			MsgType: uspproto.Header_GET_RESP,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Response{
				Response: &uspproto.Response{
					RespType: &uspproto.Response_GetResp{
						GetResp: &uspproto.GetResp{},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	_, err = DecodeObjectDeletion(msg)
	if !errors.Is(err, ErrNotObjectDeletion) {
		t.Errorf("DecodeObjectDeletion returned %v, want ErrNotObjectDeletion", err)
	}
}

func TestDecodeObjectDeletionNilPayload(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-od-nil",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-od-nil",
							SendResp:       false,
							Notification: &uspproto.Notify_ObjDeletion{
								ObjDeletion: nil,
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	od, err := DecodeObjectDeletion(msg)
	if err != nil {
		t.Fatalf("DecodeObjectDeletion: %v (must not panic or error on nil payload)", err)
	}
	if od.ObjPath != "" {
		t.Errorf("ObjPath = %q, want empty string for nil payload", od.ObjPath)
	}
}

func TestDecodeEvent(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-event-1",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-ev-1",
							SendResp:       false,
							Notification: &uspproto.Notify_Event_{
								Event: &uspproto.Notify_Event{
									ObjPath:   "Device.",
									EventName: "Boot!",
									Params: map[string]string{
										"FirmwareVersion": "1.2.3",
									},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	ev, err := DecodeEvent(msg)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}

	if ev.SubscriptionID != "sub-ev-1" {
		t.Errorf("SubscriptionID = %q, want sub-ev-1", ev.SubscriptionID)
	}
	if ev.SendResp {
		t.Error("SendResp = true, want false")
	}
	if ev.ObjPath != "Device." {
		t.Errorf("ObjPath = %q, want Device.", ev.ObjPath)
	}
	if ev.EventName != "Boot!" {
		t.Errorf("EventName = %q, want Boot!", ev.EventName)
	}
	if len(ev.Params) != 1 || ev.Params["FirmwareVersion"] != "1.2.3" {
		t.Errorf("Params = %+v, want {FirmwareVersion: 1.2.3}", ev.Params)
	}
}

func TestDecodeEventWrongVariant(t *testing.T) {
	t.Run("OnBoardRequest", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-ev-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-ev-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_OnBoardReq{
									OnBoardReq: &uspproto.Notify_OnBoardRequest{
										Oui: "0025C2",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeEvent(msg)
		if !errors.Is(err, ErrNotEvent) {
			t.Errorf("DecodeEvent returned %v, want ErrNotEvent", err)
		}
	})

	t.Run("OperComplete", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-ev-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-ev-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_OperComplete{
									OperComplete: &uspproto.Notify_OperationComplete{
										ObjPath: "Device.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeEvent(msg)
		if !errors.Is(err, ErrNotEvent) {
			t.Errorf("DecodeEvent returned %v, want ErrNotEvent", err)
		}
	})

	t.Run("ValueChange", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-ev-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-ev-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_ValueChange_{
									ValueChange: &uspproto.Notify_ValueChange{
										ParamPath: "Device.Param",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeEvent(msg)
		if !errors.Is(err, ErrNotEvent) {
			t.Errorf("DecodeEvent returned %v, want ErrNotEvent", err)
		}
	})

	t.Run("ObjectCreation", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-ev-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-ev-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_ObjCreation{
									ObjCreation: &uspproto.Notify_ObjectCreation{
										ObjPath: "Device.WiFi.SSID.1.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeEvent(msg)
		if !errors.Is(err, ErrNotEvent) {
			t.Errorf("DecodeEvent returned %v, want ErrNotEvent", err)
		}
	})

	t.Run("ObjectDeletion", func(t *testing.T) {
		wire, err := proto.Marshal(&uspproto.Msg{
			Header: &uspproto.Header{
				MsgId:   "m-ev-wrong-variant",
				MsgType: uspproto.Header_NOTIFY,
			},
			Body: &uspproto.Body{
				MsgBody: &uspproto.Body_Request{
					Request: &uspproto.Request{
						ReqType: &uspproto.Request_Notify{
							Notify: &uspproto.Notify{
								SubscriptionId: "sub-ev-wrong",
								SendResp:       false,
								Notification: &uspproto.Notify_ObjDeletion{
									ObjDeletion: &uspproto.Notify_ObjectDeletion{
										ObjPath: "Device.WiFi.SSID.1.",
									},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeMsg(wire)
		if err != nil {
			t.Fatalf("DecodeMsg: %v", err)
		}
		_, err = DecodeEvent(msg)
		if !errors.Is(err, ErrNotEvent) {
			t.Errorf("DecodeEvent returned %v, want ErrNotEvent", err)
		}
	})
}

func TestDecodeEventWrongMsgType(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-ev-getresp",
			MsgType: uspproto.Header_GET_RESP,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Response{
				Response: &uspproto.Response{
					RespType: &uspproto.Response_GetResp{
						GetResp: &uspproto.GetResp{},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	_, err = DecodeEvent(msg)
	if !errors.Is(err, ErrNotEvent) {
		t.Errorf("DecodeEvent returned %v, want ErrNotEvent", err)
	}
}

func TestDecodeEventNilPayload(t *testing.T) {
	wire, err := proto.Marshal(&uspproto.Msg{
		Header: &uspproto.Header{
			MsgId:   "m-ev-nil",
			MsgType: uspproto.Header_NOTIFY,
		},
		Body: &uspproto.Body{
			MsgBody: &uspproto.Body_Request{
				Request: &uspproto.Request{
					ReqType: &uspproto.Request_Notify{
						Notify: &uspproto.Notify{
							SubscriptionId: "sub-ev-nil",
							SendResp:       false,
							Notification: &uspproto.Notify_Event_{
								Event: nil,
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := DecodeMsg(wire)
	if err != nil {
		t.Fatalf("DecodeMsg: %v", err)
	}

	ev, err := DecodeEvent(msg)
	if err != nil {
		t.Fatalf("DecodeEvent: %v (must not panic or error on nil payload)", err)
	}
	if ev.ObjPath != "" {
		t.Errorf("ObjPath = %q, want empty string for nil payload", ev.ObjPath)
	}
	if ev.EventName != "" {
		t.Errorf("EventName = %q, want empty string for nil payload", ev.EventName)
	}
	if ev.Params != nil {
		t.Errorf("Params = %+v, want nil for nil payload", ev.Params)
	}
}
