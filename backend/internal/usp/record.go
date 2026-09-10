package usp

import (
	"errors"
	"fmt"
	"slices"

	"acs/internal/usp/uspproto"

	"google.golang.org/protobuf/proto"
)

// RecordVersion is the USP version this controller stamps on every
// record it sends.
//
// 1.3 deliberately, not 1.4 or 1.5: the Broadband Forum reference agent
// emits Record.version "1.3" and advertises "1.0,1.1,1.2,1.3" even in
// releases whose notes headline "USP 1.4" (design §3.4). A controller
// that demanded 1.4 would fail against the reference implementation.
const RecordVersion = "1.3"

// SupportedRecordVersions are the versions this controller accepts on
// inbound records.
var SupportedRecordVersions = []string{"1.0", "1.1", "1.2", "1.3"}

var (
	ErrMalformedRecord            = errors.New("malformed USP record")
	ErrUnsupportedRecordVersion   = errors.New("unsupported USP record version")
	ErrPayloadSecurityUnsupported = errors.New("non-plaintext USP payload security is not supported")
	ErrNotAddressedToUs           = errors.New("USP record is not addressed to this controller")
	ErrNoPayload                  = errors.New("USP record carries no message payload")
	ErrSessionContextUnsupported  = errors.New("USP session context records are not supported")
)

// RecordType identifies which oneof branch of uspproto.Record a decoded
// record carried, so callers can tell a connect/disconnect lifecycle
// event from a session-context record from an actual message.
type RecordType int

const (
	RecordUnknown RecordType = iota
	RecordNoSessionContext
	RecordSessionContext
	RecordWebSocketConnect
	RecordMQTTConnect
	RecordSTOMPConnect
	RecordUDSConnect
	RecordDisconnect
)

func (t RecordType) String() string {
	switch t {
	case RecordNoSessionContext:
		return "NoSessionContext"
	case RecordSessionContext:
		return "SessionContext"
	case RecordWebSocketConnect:
		return "WebSocketConnect"
	case RecordMQTTConnect:
		return "MQTTConnect"
	case RecordSTOMPConnect:
		return "STOMPConnect"
	case RecordUDSConnect:
		return "UDSConnect"
	case RecordDisconnect:
		return "Disconnect"
	default:
		return "Unknown"
	}
}

// EncodeRecord wraps a marshalled Msg in a Record addressed from us to
// the agent.
//
// Always a NoSessionContextRecord: session context exists for
// segmentation and end-to-end encryption, both of which are out of scope
// (design §10) and rejected by the reference agent anyway.
func EncodeRecord(from, to EndpointID, payload []byte) ([]byte, error) {
	wire, err := proto.Marshal(&uspproto.Record{
		Version:         RecordVersion,
		ToId:            string(to),
		FromId:          string(from),
		PayloadSecurity: uspproto.Record_PLAINTEXT,
		RecordType: &uspproto.Record_NoSessionContext{
			NoSessionContext: &uspproto.NoSessionContextRecord{Payload: payload},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal USP record: %w", err)
	}
	return wire, nil
}

// DecodedRecord is a validated inbound record with its message payload
// extracted.
type DecodedRecord struct {
	From    EndpointID
	To      EndpointID
	Version string
	Payload []byte
	// Type identifies which oneof branch of the wire record produced
	// this value. It is always set, including when an error is also
	// returned (see DecodeRecord).
	Type RecordType
	// DisconnectReason and DisconnectCode are populated only when Type
	// is RecordDisconnect.
	DisconnectReason string
	DisconnectCode   uint32
}

// DecodeRecord unmarshals and validates an inbound record, returning the
// inner message payload.
//
// us is this controller's own endpoint id. A record addressed elsewhere
// is refused rather than processed: the reference agent applies the same
// rule in the other direction, and honouring a misaddressed record would
// make the endpoint id meaningless as an access control.
//
// A connect or disconnect record is well-formed but carries no message;
// callers get ErrNoPayload alongside a non-nil *DecodedRecord whose Type
// (and, for a disconnect, DisconnectReason/DisconnectCode) identifies
// the lifecycle event, so they can errors.Is(err, ErrNoPayload) and then
// inspect the record rather than discard it. A session-context record is
// an unsupported feature, not an empty message: it is reported as
// ErrSessionContextUnsupported, also with a non-nil record carrying
// Type == RecordSessionContext. Every other error path returns a nil
// record.
func DecodeRecord(wire []byte, us EndpointID) (*DecodedRecord, error) {
	var rec uspproto.Record
	if err := proto.Unmarshal(wire, &rec); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedRecord, err)
	}
	if !slices.Contains(SupportedRecordVersions, rec.GetVersion()) {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedRecordVersion, rec.GetVersion())
	}
	if rec.GetPayloadSecurity() != uspproto.Record_PLAINTEXT {
		return nil, fmt.Errorf("%w: %v", ErrPayloadSecurityUnsupported, rec.GetPayloadSecurity())
	}
	if rec.GetToId() != string(us) {
		return nil, fmt.Errorf("%w: addressed to %q, we are %q", ErrNotAddressedToUs, rec.GetToId(), us)
	}

	out := &DecodedRecord{
		From:    EndpointID(rec.GetFromId()),
		To:      EndpointID(rec.GetToId()),
		Version: rec.GetVersion(),
	}

	switch rt := rec.GetRecordType().(type) {
	case *uspproto.Record_NoSessionContext:
		out.Type = RecordNoSessionContext
		payload := rt.NoSessionContext.GetPayload()
		if len(payload) == 0 {
			return out, fmt.Errorf("%w: empty no-session-context payload", ErrNoPayload)
		}
		out.Payload = payload
		return out, nil
	case *uspproto.Record_SessionContext:
		out.Type = RecordSessionContext
		return out, fmt.Errorf("%w", ErrSessionContextUnsupported)
	case *uspproto.Record_WebsocketConnect:
		out.Type = RecordWebSocketConnect
		return out, fmt.Errorf("%w: record type %T", ErrNoPayload, rt)
	case *uspproto.Record_MqttConnect:
		out.Type = RecordMQTTConnect
		return out, fmt.Errorf("%w: record type %T", ErrNoPayload, rt)
	case *uspproto.Record_StompConnect:
		out.Type = RecordSTOMPConnect
		return out, fmt.Errorf("%w: record type %T", ErrNoPayload, rt)
	case *uspproto.Record_UdsConnect:
		out.Type = RecordUDSConnect
		return out, fmt.Errorf("%w: record type %T", ErrNoPayload, rt)
	case *uspproto.Record_Disconnect:
		out.Type = RecordDisconnect
		out.DisconnectReason = rt.Disconnect.GetReason()
		out.DisconnectCode = rt.Disconnect.GetReasonCode()
		return out, fmt.Errorf("%w: record type %T", ErrNoPayload, rt)
	default:
		out.Type = RecordUnknown
		return out, fmt.Errorf("%w: record type %T", ErrNoPayload, rt)
	}
}
