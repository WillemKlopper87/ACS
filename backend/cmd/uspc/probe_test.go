package main

import (
	"context"
	"log/slog"
	"testing"

	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"

	"google.golang.org/protobuf/proto"
)

// mtpKindForTest keeps captureConn's Kind method signature legible
// without this file importing mtp just to spell out mtp.Conn's exact
// return type inline.
type mtpKindForTest = mtp.Kind

// captureConn records what the probe sends so the test can read the
// msg_id back out of the Get and answer it. Its Kind() must return the
// same underlying type as mtp.Conn's, hence the alias above -- a
// distinct defined type would not satisfy the interface.
var _ mtp.Conn = (*captureConn)(nil)

type captureConn struct {
	id     usp.EndpointID
	sent   [][]byte
	closed []string
}

func (c *captureConn) Endpoint() usp.EndpointID { return c.id }
func (c *captureConn) Kind() mtpKindForTest     { return "WebSocket" }
func (c *captureConn) RemoteAddr() string       { return "test" }
func (c *captureConn) Close(reason string) error {
	c.closed = append(c.closed, reason)
	return nil
}
func (c *captureConn) Send(_ context.Context, r []byte) error { c.sent = append(c.sent, r); return nil }

func sentMsgID(t *testing.T, controller, agent usp.EndpointID, wire []byte) string {
	t.Helper()
	rec, err := usp.DecodeRecord(wire, agent) // the agent is the recipient
	if err != nil {
		t.Fatalf("decode probe record: %v", err)
	}
	if rec.From != controller {
		t.Fatalf("probe record From = %q, want %q", rec.From, controller)
	}
	msg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if msg.GetHeader().GetMsgType() != uspproto.Header_GET {
		t.Fatalf("probe sent %v, want GET", msg.GetHeader().GetMsgType())
	}
	if paths := msg.GetBody().GetRequest().GetGet().GetParamPaths(); len(paths) != 1 || paths[0] != "Device.DeviceInfo." {
		t.Fatalf("probe Get paths = %v, want [Device.DeviceInfo.]", paths)
	}
	return msg.GetHeader().GetMsgId()
}

func getResp(msgID string, params map[string]string) *uspproto.Msg {
	results := []*uspproto.GetResp_ResolvedPathResult{{ResolvedPath: "Device.DeviceInfo.", ResultParams: params}}
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: msgID, MsgType: uspproto.Header_GET_RESP},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Response{Response: &uspproto.Response{
			RespType: &uspproto.Response_GetResp{GetResp: &uspproto.GetResp{
				ReqPathResults: []*uspproto.GetResp_RequestedPathResult{{RequestedPath: "Device.DeviceInfo.", ResolvedPathResults: results}},
			}},
		}}},
	}
}

const (
	ctrl  = usp.EndpointID("self::acs-test")
	agent = usp.EndpointID("os::012345-AAAA")
)

func TestProbeMatchesResponse(t *testing.T) {
	p := newProbe(ctrl, slog.Default())
	c := &captureConn{id: agent}
	if err := p.start(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("probe sent %d records, want 1", len(c.sent))
	}
	id := sentMsgID(t, ctrl, agent, c.sent[0])
	if !p.handle(agent, getResp(id, map[string]string{"SoftwareVersion": "11.0.7"})) {
		t.Error("handle returned false for the probe's own msg_id")
	}
	if p.handle(agent, getResp(id, nil)) {
		t.Error("handle matched the same msg_id twice; entries must be consumed")
	}
}

func TestProbeReportsError(t *testing.T) {
	p := newProbe(ctrl, slog.Default())
	c := &captureConn{id: agent}
	_ = p.start(context.Background(), c)
	id := sentMsgID(t, ctrl, agent, c.sent[0])
	errMsg := &uspproto.Msg{
		Header: &uspproto.Header{MsgId: id, MsgType: uspproto.Header_ERROR},
		Body:   &uspproto.Body{MsgBody: &uspproto.Body_Error{Error: &uspproto.Error{ErrCode: 7006, ErrMsg: "permission denied"}}},
	}
	if !p.handle(agent, errMsg) {
		t.Error("handle returned false for an Error body carrying the probe's msg_id")
	}
}

func TestProbeIgnoresUnknownMsgID(t *testing.T) {
	p := newProbe(ctrl, slog.Default())
	if p.handle(agent, getResp("never-sent", nil)) {
		t.Error("handle matched a msg_id the probe never sent")
	}
	// And a nil / bodiless message must not panic.
	_ = p.handle(agent, &uspproto.Msg{Header: &uspproto.Header{MsgId: "x"}})
	_ = proto.Size // keep the import honest if the file above does not otherwise use it
}
