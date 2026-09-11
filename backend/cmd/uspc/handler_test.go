package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"acs/internal/observability"
	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"

	"google.golang.org/protobuf/proto"
)

// newTestHandler builds a handler wired to store (typically a
// fakeIdentityStore), with a fresh registry/probe/metrics of its own so
// tests don't share state.
func newTestHandler(store identityStore) *handler {
	um := newUSPMetrics(observability.NewMetrics("uspc-test"))
	return &handler{
		log:          slog.Default(),
		registry:     mtp.NewRegistry(),
		probe:        newProbe(ctrl, slog.Default()),
		controllerID: ctrl,
		metrics:      um,
		reconciler:   newReconciler(store, slog.Default()),
	}
}

// recordWire marshals msg and wraps it in a USP Record wire, as if from
// had sent it to to -- the shape handler.OnRecord expects in
// mtp.Inbound.Record.
func recordWire(t *testing.T, from, to usp.EndpointID, msg *uspproto.Msg) []byte {
	t.Helper()
	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal msg: %v", err)
	}
	wire, err := usp.EncodeRecord(from, to, payload)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	return wire
}

// onBoardRequestMsg builds a NOTIFY message carrying an OnBoardRequest
// notification, matching the shape internal/usp/message_test.go's
// TestDecodeOnBoardRequest builds.
func onBoardRequestMsg(subscriptionID string, sendResp bool, oui, productClass, serialNumber string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: usp.NewMsgID(), MsgType: uspproto.Header_NOTIFY},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Request{Request: &uspproto.Request{
			ReqType: &uspproto.Request_Notify{Notify: &uspproto.Notify{
				SubscriptionId: subscriptionID,
				SendResp:       sendResp,
				Notification: &uspproto.Notify_OnBoardReq{OnBoardReq: &uspproto.Notify_OnBoardRequest{
					Oui:          oui,
					ProductClass: productClass,
					SerialNumber: serialNumber,
				}},
			}},
		}}},
	}
}

// deviceInfoParams is the ResultParams map a GetResp needs to carry (under
// resolved path "Device.DeviceInfo.", the shape probe_test.go's getResp
// helper builds) to satisfy handler's probe-fallback identity check.
func deviceInfoParams(oui, productClass, serialNumber string) map[string]string {
	return map[string]string{
		"ManufacturerOUI": oui,
		"ProductClass":    productClass,
		"SerialNumber":    serialNumber,
	}
}

func TestHandlerOnBoardRequestReconciles(t *testing.T) {
	store := newFakeIdentityStore()
	h := newTestHandler(store)
	c := &captureConn{id: agent}

	msg := onBoardRequestMsg("sub-1", false, "0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	if len(store.upsertCalls) != 1 || store.upsertCalls[0] != (onboardCall{"0025C2", "Gateway", "SN12345"}) {
		t.Fatalf("upsertCalls = %+v, want one call with the OnBoardRequest's identity", store.upsertCalls)
	}
	if len(store.linkCalls) != 1 {
		t.Fatalf("linkCalls = %+v, want exactly 1", store.linkCalls)
	}
	if got := store.linkCalls[0]; got.EndpointID != string(agent) || got.MTPKind != "WebSocket" {
		t.Errorf("linkCalls[0] = %+v, want endpoint=%q mtp=WebSocket", got, agent)
	}
	if !h.isReconciled(c) {
		t.Error("connection not marked reconciled after a successful OnBoardRequest")
	}
}

func TestHandlerOnBoardRequestSendsResp(t *testing.T) {
	store := newFakeIdentityStore()
	h := newTestHandler(store)
	c := &captureConn{id: agent}

	msg := onBoardRequestMsg("sub-42", true, "0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	if len(c.sent) != 1 {
		t.Fatalf("c.sent has %d records, want exactly 1 (the NotifyResp)", len(c.sent))
	}
	rec, err := usp.DecodeRecord(c.sent[0], agent)
	if err != nil {
		t.Fatalf("decode NotifyResp record: %v", err)
	}
	if rec.From != ctrl {
		t.Errorf("NotifyResp record From = %q, want %q", rec.From, ctrl)
	}
	respMsg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		t.Fatalf("decode NotifyResp msg: %v", err)
	}
	if respMsg.GetHeader().GetMsgType() != uspproto.Header_NOTIFY_RESP {
		t.Fatalf("msg type = %v, want NOTIFY_RESP", respMsg.GetHeader().GetMsgType())
	}
	if got := respMsg.GetBody().GetResponse().GetNotifyResp().GetSubscriptionId(); got != "sub-42" {
		t.Errorf("NotifyResp subscription_id = %q, want sub-42", got)
	}
}

func TestHandlerOnBoardRequestNoRespWhenNotRequested(t *testing.T) {
	store := newFakeIdentityStore()
	h := newTestHandler(store)
	c := &captureConn{id: agent}

	msg := onBoardRequestMsg("sub-1", false, "0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	if len(c.sent) != 0 {
		t.Fatalf("c.sent has %d records, want 0 when SendResp is false", len(c.sent))
	}
}

func TestHandlerProbeFallbackReconcilesOnce(t *testing.T) {
	store := newFakeIdentityStore()
	h := newTestHandler(store)
	c := &captureConn{id: agent}

	// Seed two outstanding probe Gets on the same connection (OnConnect's
	// own probe, plus one started manually) so both GetResps below
	// genuinely match the probe -- exercising the reconciler-level dedup,
	// not just the probe's own single-match-per-msg_id behaviour.
	h.OnConnect(c)
	if err := h.probe.start(context.Background(), c); err != nil {
		t.Fatalf("probe.start: %v", err)
	}
	if len(c.sent) != 2 {
		t.Fatalf("c.sent has %d probe Gets, want 2", len(c.sent))
	}
	id1 := sentMsgID(t, ctrl, agent, c.sent[0])
	id2 := sentMsgID(t, ctrl, agent, c.sent[1])

	params := deviceInfoParams("0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, getResp(id1, params))})
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, getResp(id2, params))})

	if len(store.upsertCalls) != 1 {
		t.Fatalf("upsertCalls = %+v, want exactly 1 despite two matching GetResps", store.upsertCalls)
	}
	if len(store.linkCalls) != 1 {
		t.Fatalf("linkCalls = %+v, want exactly 1", store.linkCalls)
	}
}

func TestHandlerOnBoardRequestSuppressesProbeFallback(t *testing.T) {
	store := newFakeIdentityStore()
	h := newTestHandler(store)
	c := &captureConn{id: agent}

	h.OnConnect(c) // starts one outstanding probe Get
	if len(c.sent) != 1 {
		t.Fatalf("c.sent has %d probe Gets, want 1", len(c.sent))
	}
	probeMsgID := sentMsgID(t, ctrl, agent, c.sent[0])

	onboard := onBoardRequestMsg("sub-1", false, "0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, onboard)})

	// Now the probe's own GetResp arrives, also carrying DeviceInfo
	// identity params -- it must not trigger a second reconciliation.
	params := deviceInfoParams("0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, getResp(probeMsgID, params))})

	if len(store.upsertCalls) != 1 {
		t.Fatalf("upsertCalls = %+v, want exactly 1 (OnBoardRequest only, probe fallback suppressed)", store.upsertCalls)
	}
}

func TestHandlerDisconnectMarksUspAgent(t *testing.T) {
	store := newFakeIdentityStore()
	h := newTestHandler(store)
	c := &captureConn{id: agent}

	h.OnConnect(c)
	msg := onBoardRequestMsg("sub-1", false, "0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	deviceID, ok := h.reconciledDeviceID(c)
	if !ok {
		t.Fatal("connection not reconciled before disconnect")
	}

	h.OnDisconnect(c, nil)

	if len(store.disconnectCalls) != 1 || store.disconnectCalls[0] != deviceID {
		t.Fatalf("disconnectCalls = %v, want [%s]", store.disconnectCalls, deviceID)
	}
}

func TestHandlerDisconnectSkipsUnreconciledConnection(t *testing.T) {
	store := newFakeIdentityStore()
	h := newTestHandler(store)
	c := &captureConn{id: agent}

	h.OnConnect(c) // never reconciled: no OnBoardRequest, no matching probe GetResp
	h.OnDisconnect(c, nil)

	if len(store.disconnectCalls) != 0 {
		t.Fatalf("disconnectCalls = %v, want none for an unreconciled connection", store.disconnectCalls)
	}
}

// TestHandlerDisconnectSkipsStaleConnectionAfterTakeover proves the
// registry-removal guard in OnDisconnect: when an agent reconnects (a new
// Conn for the same endpoint id replaces the old one via OnConnect's own
// registry.Add), the *old* Conn's own OnDisconnect can still fire
// afterward -- e.g. its transport's read goroutine noticing the socket
// closed only after the new connection has already taken over. That
// stale disconnect must not call MarkUspAgentDisconnected for a device
// the new connection just (re-)linked as connected; only a disconnect
// for the Conn actually still on record in the registry may do that.
func TestHandlerDisconnectSkipsStaleConnectionAfterTakeover(t *testing.T) {
	store := newFakeIdentityStore()
	h := newTestHandler(store)

	a := &captureConn{id: agent}
	h.OnConnect(a)
	msg := onBoardRequestMsg("sub-1", false, "0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: a, Record: recordWire(t, agent, ctrl, msg)})

	deviceID, ok := h.reconciledDeviceID(a)
	if !ok {
		t.Fatal("connection a not reconciled before takeover")
	}

	// b reconnects as the same agent (same endpoint id). OnConnect's own
	// registry.Add replaces a with b as the live connection for "agent".
	b := &captureConn{id: agent}
	h.OnConnect(b)
	msg2 := onBoardRequestMsg("sub-2", false, "0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: b, Record: recordWire(t, agent, ctrl, msg2)})
	if _, ok := h.reconciledDeviceID(b); !ok {
		t.Fatal("connection b not reconciled after its own OnBoardRequest")
	}

	// a's own (stale, delayed) disconnect callback arrives after b has
	// already taken over. It must be a no-op for identity: it must not
	// mark the device disconnected, since b is the one actually live now.
	h.OnDisconnect(a, errors.New("stale: replaced by new connection"))

	if len(store.disconnectCalls) != 0 {
		t.Fatalf("disconnectCalls = %v, want none: a's disconnect is stale (b already replaced it in the registry)", store.disconnectCalls)
	}

	// A genuine disconnect for b, the connection actually on record,
	// still works correctly afterward.
	h.OnDisconnect(b, nil)
	if len(store.disconnectCalls) != 1 || store.disconnectCalls[0] != deviceID {
		t.Fatalf("disconnectCalls = %v, want [%s] after b's genuine disconnect", store.disconnectCalls, deviceID)
	}
}
