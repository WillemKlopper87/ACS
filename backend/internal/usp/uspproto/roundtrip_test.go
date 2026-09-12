package uspproto

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestRecordRoundTrip is a smoke test that the generated bindings are
// wired up: a Record with a NoSessionContext payload survives
// marshal/unmarshal with its oneof intact. It is deliberately shallow --
// the protobuf runtime is not ours to test. What it catches is a codegen
// or M-flag mistake that produces types which compile but do not encode.
func TestRecordRoundTrip(t *testing.T) {
	in := &Record{
		Version:         "1.3",
		ToId:            "os::012345-0800270B57FF",
		FromId:          "self::acs-controller",
		PayloadSecurity: Record_PLAINTEXT,
		RecordType: &Record_NoSessionContext{
			NoSessionContext: &NoSessionContextRecord{Payload: []byte("hello")},
		},
	}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Record
	if err := proto.Unmarshal(wire, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Version != "1.3" || out.ToId != in.ToId || out.FromId != in.FromId {
		t.Errorf("envelope fields lost: %+v", &out)
	}
	if out.PayloadSecurity != Record_PLAINTEXT {
		t.Errorf("PayloadSecurity = %v, want PLAINTEXT", out.PayloadSecurity)
	}
	nsc, ok := out.GetRecordType().(*Record_NoSessionContext)
	if !ok {
		t.Fatalf("record_type oneof = %T, want *Record_NoSessionContext", out.GetRecordType())
	}
	if string(nsc.NoSessionContext.GetPayload()) != "hello" {
		t.Errorf("payload = %q, want %q", nsc.NoSessionContext.GetPayload(), "hello")
	}
}

// TestMsgRoundTrip covers the second proto package, proving both files
// landed in one Go package with their oneofs usable together.
func TestMsgRoundTrip(t *testing.T) {
	in := &Msg{
		Header: &Header{MsgId: "m-1", MsgType: Header_GET},
		Body: &Body{MsgBody: &Body_Request{Request: &Request{
			ReqType: &Request_Get{Get: &Get{ParamPaths: []string{"Device.DeviceInfo."}, MaxDepth: 1}},
		}}},
	}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Msg
	if err := proto.Unmarshal(wire, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetHeader().GetMsgId() != "m-1" || out.GetHeader().GetMsgType() != Header_GET {
		t.Errorf("header lost: %+v", out.GetHeader())
	}
	got := out.GetBody().GetRequest().GetGet().GetParamPaths()
	if len(got) != 1 || got[0] != "Device.DeviceInfo." {
		t.Errorf("param_paths = %v, want [Device.DeviceInfo.]", got)
	}
}
