package usp

import (
	"errors"
	"testing"

	"acs/internal/usp/uspproto"

	"google.golang.org/protobuf/proto"
)

const (
	testAgent      = EndpointID("os::012345-0800270B57FF")
	testController = EndpointID("self::acs-controller")
)

func TestEncodeDecodeRecordRoundTrip(t *testing.T) {
	wire, err := EncodeRecord(testController, testAgent, []byte("payload-bytes"))
	if err != nil {
		t.Fatalf("EncodeRecord: %v", err)
	}
	// Decode as the agent would: the record is addressed to it.
	got, err := DecodeRecord(wire, testAgent)
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	if got.From != testController || got.To != testAgent {
		t.Errorf("addresses = %q -> %q, want %q -> %q", got.From, got.To, testController, testAgent)
	}
	if got.Version != RecordVersion {
		t.Errorf("Version = %q, want %q", got.Version, RecordVersion)
	}
	if string(got.Payload) != "payload-bytes" {
		t.Errorf("Payload = %q, want payload-bytes", got.Payload)
	}
}

// The reference agent emits Record.version "1.3" and advertises up to
// 1.3 despite release notes headlining "USP 1.4". Accepting 1.0-1.3 and
// rejecting anything else is the contract.
func TestDecodeRecordVersionAcceptance(t *testing.T) {
	for _, v := range []string{"1.0", "1.1", "1.2", "1.3"} {
		wire := mustMarshalRecord(t, &uspproto.Record{
			Version: v, ToId: string(testController), FromId: string(testAgent),
			PayloadSecurity: uspproto.Record_PLAINTEXT,
			RecordType: &uspproto.Record_NoSessionContext{
				NoSessionContext: &uspproto.NoSessionContextRecord{Payload: []byte("x")},
			},
		})
		if _, err := DecodeRecord(wire, testController); err != nil {
			t.Errorf("version %q rejected: %v", v, err)
		}
	}
	wire := mustMarshalRecord(t, &uspproto.Record{
		Version: "2.0", ToId: string(testController), FromId: string(testAgent),
		PayloadSecurity: uspproto.Record_PLAINTEXT,
		RecordType: &uspproto.Record_NoSessionContext{
			NoSessionContext: &uspproto.NoSessionContextRecord{Payload: []byte("x")},
		},
	})
	if _, err := DecodeRecord(wire, testController); !errors.Is(err, ErrUnsupportedRecordVersion) {
		t.Errorf("version 2.0 gave %v, want ErrUnsupportedRecordVersion", err)
	}
}

// A record addressed to a different endpoint must be refused outright,
// mirroring the reference agent's own check.
func TestDecodeRecordRejectsWrongRecipient(t *testing.T) {
	wire := mustMarshalRecord(t, &uspproto.Record{
		Version: RecordVersion, ToId: "os::999999-SOMEONEELSE", FromId: string(testAgent),
		PayloadSecurity: uspproto.Record_PLAINTEXT,
		RecordType: &uspproto.Record_NoSessionContext{
			NoSessionContext: &uspproto.NoSessionContextRecord{Payload: []byte("x")},
		},
	})
	if _, err := DecodeRecord(wire, testController); !errors.Is(err, ErrNotAddressedToUs) {
		t.Errorf("got %v, want ErrNotAddressedToUs", err)
	}
}

// E2E/encrypted payloads are out of scope and the reference agent
// rejects them too, so we must not silently treat one as plaintext.
func TestDecodeRecordRejectsNonPlaintextSecurity(t *testing.T) {
	wire := mustMarshalRecord(t, &uspproto.Record{
		Version: RecordVersion, ToId: string(testController), FromId: string(testAgent),
		PayloadSecurity: uspproto.Record_TLS12,
		RecordType: &uspproto.Record_NoSessionContext{
			NoSessionContext: &uspproto.NoSessionContextRecord{Payload: []byte("x")},
		},
	})
	if _, err := DecodeRecord(wire, testController); !errors.Is(err, ErrPayloadSecurityUnsupported) {
		t.Errorf("got %v, want ErrPayloadSecurityUnsupported", err)
	}
}

// A connect or disconnect record carries no message. Callers must be
// able to tell that apart from a decode failure.
func TestDecodeRecordNoPayload(t *testing.T) {
	for name, rt := range map[string]any{
		"websocket_connect": &uspproto.Record_WebsocketConnect{WebsocketConnect: &uspproto.WebSocketConnectRecord{}},
		"disconnect":        &uspproto.Record_Disconnect{Disconnect: &uspproto.DisconnectRecord{Reason: "bye"}},
		// A NoSessionContext record whose payload is empty is a distinct
		// branch from a connect/disconnect record and must also be ErrNoPayload.
		"empty_no_session_context": &uspproto.Record_NoSessionContext{NoSessionContext: &uspproto.NoSessionContextRecord{Payload: nil}},
	} {
		rec := &uspproto.Record{
			Version: RecordVersion, ToId: string(testController), FromId: string(testAgent),
			PayloadSecurity: uspproto.Record_PLAINTEXT,
		}
		switch v := rt.(type) {
		case *uspproto.Record_WebsocketConnect:
			rec.RecordType = v
		case *uspproto.Record_Disconnect:
			rec.RecordType = v
		case *uspproto.Record_NoSessionContext:
			rec.RecordType = v
		}
		got, err := DecodeRecord(mustMarshalRecord(t, rec), testController)
		if !errors.Is(err, ErrNoPayload) {
			t.Errorf("%s: got %v, want ErrNoPayload", name, err)
		}
		if got == nil {
			t.Fatalf("%s: record is nil; a lifecycle record must be returned so its type can be read", name)
		}
		if name == "empty_no_session_context" && got.Type != RecordNoSessionContext {
			t.Errorf("%s: Type = %v, want RecordNoSessionContext", name, got.Type)
		}
	}
}

func TestDecodeRecordConnectTypes(t *testing.T) {
	cases := map[string]struct {
		rt   any
		want RecordType
	}{
		"websocket": {&uspproto.Record_WebsocketConnect{WebsocketConnect: &uspproto.WebSocketConnectRecord{}}, RecordWebSocketConnect},
		"mqtt":      {&uspproto.Record_MqttConnect{MqttConnect: &uspproto.MQTTConnectRecord{Version: uspproto.MQTTConnectRecord_V5, SubscribedTopic: "/usp/agent"}}, RecordMQTTConnect},
		"stomp":     {&uspproto.Record_StompConnect{StompConnect: &uspproto.STOMPConnectRecord{}}, RecordSTOMPConnect},
		"uds":       {&uspproto.Record_UdsConnect{UdsConnect: &uspproto.UDSConnectRecord{}}, RecordUDSConnect},
	}
	for name, c := range cases {
		rec := &uspproto.Record{Version: RecordVersion, ToId: string(testController), FromId: string(testAgent), PayloadSecurity: uspproto.Record_PLAINTEXT}
		switch v := c.rt.(type) {
		case *uspproto.Record_WebsocketConnect:
			rec.RecordType = v
		case *uspproto.Record_MqttConnect:
			rec.RecordType = v
		case *uspproto.Record_StompConnect:
			rec.RecordType = v
		case *uspproto.Record_UdsConnect:
			rec.RecordType = v
		}
		got, err := DecodeRecord(mustMarshalRecord(t, rec), testController)
		if !errors.Is(err, ErrNoPayload) {
			t.Errorf("%s: err = %v, want ErrNoPayload", name, err)
		}
		if got == nil {
			t.Fatalf("%s: record is nil; a connect record must be returned so its type can be read", name)
		}
		if got.Type != c.want {
			t.Errorf("%s: Type = %v, want %v", name, got.Type, c.want)
		}
		if got.From != testAgent {
			t.Errorf("%s: From = %q, want %q -- the sender must be known even without a payload", name, got.From, testAgent)
		}
	}
}

func TestDecodeRecordDisconnectCarriesReason(t *testing.T) {
	rec := &uspproto.Record{
		Version: RecordVersion, ToId: string(testController), FromId: string(testAgent),
		PayloadSecurity: uspproto.Record_PLAINTEXT,
		RecordType:      &uspproto.Record_Disconnect{Disconnect: &uspproto.DisconnectRecord{Reason: "agent shutting down", ReasonCode: 7005}},
	}
	got, err := DecodeRecord(mustMarshalRecord(t, rec), testController)
	if !errors.Is(err, ErrNoPayload) {
		t.Fatalf("err = %v, want ErrNoPayload", err)
	}
	if got == nil || got.Type != RecordDisconnect {
		t.Fatalf("got %+v, want a RecordDisconnect", got)
	}
	if got.DisconnectReason != "agent shutting down" || got.DisconnectCode != 7005 {
		t.Errorf("disconnect detail = (%q, %d), want (agent shutting down, 7005)", got.DisconnectReason, got.DisconnectCode)
	}
}

// Session context exists for segmentation and end-to-end encryption, both
// out of scope and both refused by the reference agent. It must be
// refused distinctly, not mistaken for an empty message.
func TestDecodeRecordSessionContextUnsupported(t *testing.T) {
	rec := &uspproto.Record{
		Version: RecordVersion, ToId: string(testController), FromId: string(testAgent),
		PayloadSecurity: uspproto.Record_PLAINTEXT,
		RecordType:      &uspproto.Record_SessionContext{SessionContext: &uspproto.SessionContextRecord{SessionId: 1, Payload: [][]byte{[]byte("x")}}},
	}
	got, err := DecodeRecord(mustMarshalRecord(t, rec), testController)
	if !errors.Is(err, ErrSessionContextUnsupported) {
		t.Errorf("err = %v, want ErrSessionContextUnsupported", err)
	}
	if errors.Is(err, ErrNoPayload) {
		t.Error("a session-context record must not be reported as ErrNoPayload; it carries a payload we refuse")
	}
	if got == nil || got.Type != RecordSessionContext {
		t.Errorf("got %+v, want Type RecordSessionContext", got)
	}
}

func TestRecordTypeString(t *testing.T) {
	if RecordDisconnect.String() == "" || RecordUnknown.String() == "" {
		t.Error("RecordType.String() must never be empty; it is logged")
	}
	if RecordType(999).String() == RecordUnknown.String() && RecordUnknown.String() == "" {
		t.Error("unmapped types must render")
	}
}

func TestDecodeRecordGarbage(t *testing.T) {
	if _, err := DecodeRecord([]byte{0xff, 0xff, 0xff, 0xff}, testController); !errors.Is(err, ErrMalformedRecord) {
		t.Errorf("got %v, want ErrMalformedRecord", err)
	}
}

func mustMarshalRecord(t *testing.T, r *uspproto.Record) []byte {
	t.Helper()
	wire, err := proto.Marshal(r)
	if err != nil {
		t.Fatalf("marshal test record: %v", err)
	}
	return wire
}
