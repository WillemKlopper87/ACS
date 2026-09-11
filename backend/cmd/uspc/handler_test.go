package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"acs/internal/jobs"
	"acs/internal/observability"
	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"

	"google.golang.org/protobuf/proto"
)

// newTestHandler builds a handler wired to store (typically a
// fakeIdentityStore), with a fresh registry/probe/metrics of its own so
// tests don't share state. Its dispatcher is built with a nil
// *jobs.Repository -- tryDispatch treats that as "no dispatcher wired"
// and no-ops (see its own doc comment), which keeps these
// identity/reconciliation tests DB-free while still exercising
// resolveAndMarkReconciled's real dispatch-trigger call.
func newTestHandler(store identityStore) *handler {
	um := newUSPMetrics(observability.NewMetrics("uspc-test"))
	registry := mtp.NewRegistry()
	return &handler{
		log:          slog.Default(),
		registry:     registry,
		probe:        newProbe(ctrl, slog.Default()),
		controllerID: ctrl,
		metrics:      um,
		reconciler:   newReconciler(store, slog.Default()),
		dispatcher:   newDispatcher(nil, store, registry, ctrl, slog.Default()),
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

// operationCompleteMsg builds a NOTIFY message carrying an OperComplete
// notification, matching the shape internal/usp/message_test.go's
// TestDecodeOperationComplete builds.
func operationCompleteMsg(subscriptionID string, sendResp bool, commandKey string, outputArgs map[string]string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: usp.NewMsgID(), MsgType: uspproto.Header_NOTIFY},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Request{Request: &uspproto.Request{
			ReqType: &uspproto.Request_Notify{Notify: &uspproto.Notify{
				SubscriptionId: subscriptionID,
				SendResp:       sendResp,
				Notification: &uspproto.Notify_OperComplete{OperComplete: &uspproto.Notify_OperationComplete{
					ObjPath:     "Device.",
					CommandName: "Device.Reboot()",
					CommandKey:  commandKey,
					OperationResp: &uspproto.Notify_OperationComplete_ReqOutputArgs{
						ReqOutputArgs: &uspproto.Notify_OperationComplete_OutputArgs{OutputArgs: outputArgs},
					},
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

	if len(store.disconnectCalls) != 1 || store.disconnectCalls[0] != (disconnectCall{deviceID, string(agent)}) {
		t.Fatalf("disconnectCalls = %v, want [{%s %s}]", store.disconnectCalls, deviceID, agent)
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
	if len(store.disconnectCalls) != 1 || store.disconnectCalls[0] != (disconnectCall{deviceID, string(agent)}) {
		t.Fatalf("disconnectCalls = %v, want [{%s %s}] after b's genuine disconnect", store.disconnectCalls, deviceID, agent)
	}
}

// TestHandlerDisconnectStaleEndpointAfterDifferentEndpointReconnect covers
// final-review finding 3's exact scenario: a device reconnects under a
// DIFFERENT endpoint id (not the registry-takeover case above, where both
// connections share one endpoint id). registry.Add for the new endpoint
// id returns nil (it's a different key from the old one, so the registry
// itself never closes/supersedes the old connection), and the OLD
// endpoint's connection genuinely disconnects afterward on its own --
// registry.Remove for it returns true, by the registry's own accounting,
// since nothing ever superseded it there. That must still not clear
// `connected` on the device's single usp_agents row, because the row was
// already retargeted to the NEW endpoint id by the reconnect's own
// LinkUspAgent: only the endpoint-aware WHERE clause (not the registry
// guard) protects the device in this shape.
func TestHandlerDisconnectStaleEndpointAfterDifferentEndpointReconnect(t *testing.T) {
	store := newFakeIdentityStore()
	h := newTestHandler(store)

	oldEndpoint := usp.EndpointID("os::012345-AAAA-old")
	newEndpoint := usp.EndpointID("os::012345-AAAA-new")

	a := &captureConn{id: oldEndpoint}
	h.OnConnect(a)
	msg := onBoardRequestMsg("sub-1", false, "0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: a, Record: recordWire(t, oldEndpoint, ctrl, msg)})
	deviceID, ok := h.reconciledDeviceID(a)
	if !ok {
		t.Fatal("connection a not reconciled before reconnect")
	}

	// The same device reconnects under a DIFFERENT endpoint id. This is a
	// distinct mtp.Conn/registry entry from a -- registry.Add(b) does not
	// touch a's registry entry at all, since they key on different
	// endpoint ids.
	b := &captureConn{id: newEndpoint}
	h.OnConnect(b)
	msg2 := onBoardRequestMsg("sub-2", false, "0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: b, Record: recordWire(t, newEndpoint, ctrl, msg2)})
	if _, ok := h.reconciledDeviceID(b); !ok {
		t.Fatal("connection b not reconciled after its own OnBoardRequest")
	}

	// a's own connection now disconnects for real (e.g. the agent's old
	// MTP session finally times out). registry.Remove(a) genuinely
	// returns true here -- a was never superseded in the registry, since
	// b lives under a different key.
	h.OnDisconnect(a, nil)

	agentRow, err := store.GetUspAgentByEndpointID(context.Background(), string(newEndpoint))
	if err != nil {
		t.Fatalf("get usp_agents row after a's disconnect: %v", err)
	}
	if agentRow.DeviceID != deviceID {
		t.Fatalf("usp_agents row device id = %s, want %s", agentRow.DeviceID, deviceID)
	}
	if !agentRow.Connected {
		t.Error("device marked disconnected via a's stale old-endpoint teardown, want still connected: b's session under the new endpoint id is still live")
	}
}

// TestHandlerOperationCompleteSendsResp covers the checklist's SendResp
// row for the async completion path: an OperationComplete Notify with
// SendResp true, from a connection already reconciled to the job's own
// device, resolves the job AND sends back a NotifyResp -- exactly
// mirroring handleOnBoardRequest's own SendResp handling. Needs a real
// jobs.Repository (handleOperationComplete's ByCommandKey lookup), so
// this builds its own handler rather than using newTestHandler's nil-
// jobsRepo dispatcher.
func TestHandlerOperationCompleteSendsResp(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	job, err := jobsRepo.Create(ctx, deviceID, jobs.TypeReboot, jobs.RebootPayload{}, "test")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := jobsRepo.LeaseForTypes(ctx, deviceID, []string{jobs.TypeReboot}); err != nil {
		t.Fatalf("lease job: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c := &captureConn{id: agent}
	registerAgent(store, registry, deviceID, c)

	h := &handler{
		log:          slog.Default(),
		registry:     registry,
		probe:        newProbe(ctrl, slog.Default()),
		controllerID: ctrl,
		metrics:      newUSPMetrics(observability.NewMetrics("uspc-test")),
		reconciler:   newReconciler(store, slog.Default()),
		dispatcher:   newDispatcher(jobsRepo, store, registry, ctrl, slog.Default()),
	}
	// c is reconciled to deviceID as if an earlier OnBoardRequest had
	// already run -- handleOperationComplete's identity check is what
	// this test exercises, not reconciliation itself.
	h.markReconciled(c, deviceID)

	msg := operationCompleteMsg("sub-oc-1", true, job.CommandKey, map[string]string{"Status": "Complete"})
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
	if got := respMsg.GetBody().GetResponse().GetNotifyResp().GetSubscriptionId(); got != "sub-oc-1" {
		t.Errorf("NotifyResp subscription_id = %q, want sub-oc-1", got)
	}

	gotJob, err := jobsRepo.ByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if gotJob.Status != jobs.StatusSuccess {
		t.Errorf("job status = %s, want SUCCESS: the OperationComplete must have resolved the job in addition to sending a NotifyResp", gotJob.Status)
	}
}

// TestHandlerOperationCompleteNoRespWhenNotRequested mirrors
// TestHandlerOnBoardRequestNoRespWhenNotRequested for the OperationComplete
// path: SendResp false must not send anything back.
func TestHandlerOperationCompleteNoRespWhenNotRequested(t *testing.T) {
	jobsRepo, deviceID := newDispatcherTestDB(t)
	ctx := context.Background()
	job, err := jobsRepo.Create(ctx, deviceID, jobs.TypeReboot, jobs.RebootPayload{}, "test")
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := jobsRepo.LeaseForTypes(ctx, deviceID, []string{jobs.TypeReboot}); err != nil {
		t.Fatalf("lease job: %v", err)
	}

	store := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c := &captureConn{id: agent}
	registerAgent(store, registry, deviceID, c)

	h := &handler{
		log:          slog.Default(),
		registry:     registry,
		probe:        newProbe(ctrl, slog.Default()),
		controllerID: ctrl,
		metrics:      newUSPMetrics(observability.NewMetrics("uspc-test")),
		reconciler:   newReconciler(store, slog.Default()),
		dispatcher:   newDispatcher(jobsRepo, store, registry, ctrl, slog.Default()),
	}
	h.markReconciled(c, deviceID)

	msg := operationCompleteMsg("sub-oc-2", false, job.CommandKey, map[string]string{"Status": "Complete"})
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	if len(c.sent) != 0 {
		t.Fatalf("c.sent has %d records, want 0 when SendResp is false", len(c.sent))
	}
}
