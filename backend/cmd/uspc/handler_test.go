package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/observability"
	"acs/internal/parameters"
	"acs/internal/store"
	"acs/internal/subscriptions"
	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"

	"github.com/google/uuid"
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

// valueChangeMsg, objectCreationMsg, objectDeletionMsg and eventMsg build
// NOTIFY messages carrying each of the four remaining Notify variants,
// matching the shapes internal/usp/message_test.go's own
// TestDecodeValueChange/TestDecodeObjectCreation/TestDecodeObjectDeletion/
// TestDecodeEvent build -- same convention as onBoardRequestMsg/
// operationCompleteMsg above.
func valueChangeMsg(subscriptionID string, sendResp bool, paramPath, paramValue string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: usp.NewMsgID(), MsgType: uspproto.Header_NOTIFY},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Request{Request: &uspproto.Request{
			ReqType: &uspproto.Request_Notify{Notify: &uspproto.Notify{
				SubscriptionId: subscriptionID,
				SendResp:       sendResp,
				Notification: &uspproto.Notify_ValueChange_{ValueChange: &uspproto.Notify_ValueChange{
					ParamPath:  paramPath,
					ParamValue: paramValue,
				}},
			}},
		}}},
	}
}

func objectCreationMsg(subscriptionID string, sendResp bool, objPath string, uniqueKeys map[string]string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: usp.NewMsgID(), MsgType: uspproto.Header_NOTIFY},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Request{Request: &uspproto.Request{
			ReqType: &uspproto.Request_Notify{Notify: &uspproto.Notify{
				SubscriptionId: subscriptionID,
				SendResp:       sendResp,
				Notification: &uspproto.Notify_ObjCreation{ObjCreation: &uspproto.Notify_ObjectCreation{
					ObjPath:    objPath,
					UniqueKeys: uniqueKeys,
				}},
			}},
		}}},
	}
}

func objectDeletionMsg(subscriptionID string, sendResp bool, objPath string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: usp.NewMsgID(), MsgType: uspproto.Header_NOTIFY},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Request{Request: &uspproto.Request{
			ReqType: &uspproto.Request_Notify{Notify: &uspproto.Notify{
				SubscriptionId: subscriptionID,
				SendResp:       sendResp,
				Notification: &uspproto.Notify_ObjDeletion{ObjDeletion: &uspproto.Notify_ObjectDeletion{
					ObjPath: objPath,
				}},
			}},
		}}},
	}
}

func eventMsg(subscriptionID string, sendResp bool, objPath, eventName string, params map[string]string) *uspproto.Msg {
	return &uspproto.Msg{
		Header: &uspproto.Header{MsgId: usp.NewMsgID(), MsgType: uspproto.Header_NOTIFY},
		Body: &uspproto.Body{MsgBody: &uspproto.Body_Request{Request: &uspproto.Request{
			ReqType: &uspproto.Request_Notify{Notify: &uspproto.Notify{
				SubscriptionId: subscriptionID,
				SendResp:       sendResp,
				Notification: &uspproto.Notify_Event_{Event: &uspproto.Notify_Event{
					ObjPath:   objPath,
					EventName: eventName,
					Params:    params,
				}},
			}},
		}}},
	}
}

// newNotifyTestHandler builds a real-DB-backed handler (gated on
// ACS_TEST_POSTGRES_DSN, matching every other DB-backed suite in this
// package -- see dispatcher_test.go's newDispatcherTestDB) for the
// ValueChange/ObjectCreation/ObjectDeletion/Event tests below. Those four
// handlers write through paramsRepo/devicesRepo, both concrete
// repositories with no interface seam to fake (same justification
// newDispatcherTestDB gives for jobsRepo), so exercising them for real
// needs a real database. reconciler/dispatcher are wired against the same
// real *devices.Repository main.go itself uses for both roles (it already
// satisfies identityStore), rather than a fakeIdentityStore, since this
// helper's callers need a real devices row anyway (its device_id is a
// genuine foreign key target for parameters/device_events/
// usp_subscriptions). Returns the handler and that one pre-registered
// device's id.
func newNotifyTestHandler(t *testing.T) (*handler, string) {
	t.Helper()
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	devRepo := devices.NewRepository(db)
	dev, err := devRepo.PreRegister(ctx, "NOTIFY-TEST-01", "TestVendor", "001349", "NR7101", "SER1", nil, nil)
	if err != nil {
		t.Fatalf("pre-register device: %v", err)
	}

	registry := mtp.NewRegistry()
	h := &handler{
		log:           slog.Default(),
		registry:      registry,
		probe:         newProbe(ctrl, slog.Default()),
		controllerID:  ctrl,
		metrics:       newUSPMetrics(observability.NewMetrics("uspc-test")),
		reconciler:    newReconciler(devRepo, slog.Default()),
		dispatcher:    newDispatcher(nil, devRepo, registry, ctrl, slog.Default()),
		subscriptions: newSubscriptionReconciler(subscriptions.NewRepository(db), ctrl, slog.Default()),
		paramsRepo:    parameters.NewRepository(db),
		devicesRepo:   devRepo,
	}
	return h, dev.ID
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
	store.seedKnownDevice("0025C2", "Gateway", "SN12345")
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
	store.seedKnownDevice("0025C2", "Gateway", "SN12345")
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
	store.seedKnownDevice("0025C2", "Gateway", "SN12345")
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
	store.seedKnownDevice("0025C2", "Gateway", "SN12345")
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
	store.seedKnownDevice("0025C2", "Gateway", "SN12345")
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
	store.seedKnownDevice("0025C2", "Gateway", "SN12345")
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
	store.seedKnownDevice("0025C2", "Gateway", "SN12345")
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
	store.seedKnownDevice("0025C2", "Gateway", "SN12345")
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

// TestHandlerOnBoardRequestClosesConnectionForUnknownDevice and
// TestHandlerProbeFallbackClosesConnectionForUnknownDevice cover this
// plan's identity-level gate at the handler layer: an OnBoardRequest (or
// probe fallback) for an identity the store doesn't recognize must close
// the connection, not just fail reconciliation silently.
func TestHandlerOnBoardRequestClosesConnectionForUnknownDevice(t *testing.T) {
	store := newFakeIdentityStore() // deliberately not seeded
	h := newTestHandler(store)
	c := &captureConn{id: agent}

	msg := onBoardRequestMsg("sub-1", false, "0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	if len(c.closed) != 1 {
		t.Fatalf("c.closed = %v, want exactly 1 close call for an unknown-device OnBoardRequest", c.closed)
	}
	if h.isReconciled(c) {
		t.Error("connection marked reconciled despite an unknown-device refusal")
	}
}

func TestHandlerProbeFallbackClosesConnectionForUnknownDevice(t *testing.T) {
	store := newFakeIdentityStore() // deliberately not seeded
	h := newTestHandler(store)
	c := &captureConn{id: agent}

	h.OnConnect(c)
	if err := h.probe.start(context.Background(), c); err != nil {
		t.Fatalf("probe.start: %v", err)
	}
	if len(c.sent) != 2 {
		t.Fatalf("c.sent has %d probe Gets, want 2", len(c.sent))
	}
	id1 := sentMsgID(t, ctrl, agent, c.sent[0])

	params := deviceInfoParams("0025C2", "Gateway", "SN12345")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, getResp(id1, params))})

	if len(c.closed) != 1 {
		t.Fatalf("c.closed = %v, want exactly 1 close call for an unknown-device probe fallback", c.closed)
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

// TestHandlerValueChangeUpdatesCache covers the checklist's ValueChange
// row: a ValueChange Notify from a reconciled connection must land in
// the device's parameter cache under parameters.SourceUSPNotify.
func TestHandlerValueChangeUpdatesCache(t *testing.T) {
	h, deviceID := newNotifyTestHandler(t)
	c := &captureConn{id: agent}
	h.markReconciled(c, deviceID)

	msg := valueChangeMsg("sub-vc-1", false, "Device.WiFi.SSID.1.SSID", "MyNetwork")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	cache, err := h.paramsRepo.Get(context.Background(), deviceID)
	if err != nil {
		t.Fatalf("Get parameter cache: %v", err)
	}
	cv, ok := cache["Device.WiFi.SSID.1.SSID"]
	if !ok {
		t.Fatalf("parameter cache = %+v, want an entry for Device.WiFi.SSID.1.SSID", cache)
	}
	if cv.Value != "MyNetwork" {
		t.Errorf("cached value = %q, want MyNetwork", cv.Value)
	}
	if cv.Source != parameters.SourceUSPNotify {
		t.Errorf("cached source = %q, want %q", cv.Source, parameters.SourceUSPNotify)
	}
}

// TestHandlerObjectCreationInvalidatesAndRecords covers the checklist's
// ObjectCreation row: the affected subtree's cache entries must be
// invalidated and an event recorded carrying the Notify's UniqueKeys.
func TestHandlerObjectCreationInvalidatesAndRecords(t *testing.T) {
	h, deviceID := newNotifyTestHandler(t)
	c := &captureConn{id: agent}
	h.markReconciled(c, deviceID)

	ctx := context.Background()
	// Seed a cached value under the subtree the ObjectCreation reports,
	// so this test can prove it was actually invalidated -- not merely
	// that InvalidateSubtree didn't error.
	if err := h.paramsRepo.Upsert(ctx, deviceID, map[string]parameters.CachedValue{
		"Device.WiFi.SSID.1.SSID": {Value: "stale", Source: parameters.SourceGetValues, UpdatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	msg := objectCreationMsg("sub-oc-1", false, "Device.WiFi.SSID.1.", map[string]string{"Alias": "cpe-ssid-1"})
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	cache, err := h.paramsRepo.Get(ctx, deviceID)
	if err != nil {
		t.Fatalf("Get parameter cache: %v", err)
	}
	if _, ok := cache["Device.WiFi.SSID.1.SSID"]; ok {
		t.Errorf("parameter cache = %+v, want Device.WiFi.SSID.1.SSID invalidated", cache)
	}

	events, err := h.devicesRepo.Events(ctx, deviceID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("device events = %+v, want exactly 1", events)
	}
	ev := events[0]
	if ev.ObjPath != "Device.WiFi.SSID.1." || ev.EventName != "ObjectCreation" {
		t.Errorf("event = %+v, want obj_path=Device.WiFi.SSID.1. event_name=ObjectCreation", ev)
	}
	if ev.Params["Alias"] != "cpe-ssid-1" {
		t.Errorf("event params = %+v, want Alias=cpe-ssid-1 (the Notify's UniqueKeys)", ev.Params)
	}
	// Fix round 1, Important 2: prove the recorded row's msg_id is
	// genuinely the incoming Notify's own msg_id (RecordEvent's
	// (device_id, msg_id) redelivery-dedup key), not an empty string or
	// some other fixed value that would happen to still pass every other
	// assertion above while silently breaking dedup for every event after
	// the first.
	if ev.MsgID != msg.GetHeader().GetMsgId() {
		t.Errorf("event msg_id = %q, want %q (the Notify's own msg_id)", ev.MsgID, msg.GetHeader().GetMsgId())
	}
}

// TestHandlerObjectDeletionInvalidatesAndRecords is
// TestHandlerObjectCreationInvalidatesAndRecords' ObjectDeletion
// counterpart: same subtree invalidation and event recording, but with
// no UniqueKeys (an ObjectDeletion Notify carries none).
func TestHandlerObjectDeletionInvalidatesAndRecords(t *testing.T) {
	h, deviceID := newNotifyTestHandler(t)
	c := &captureConn{id: agent}
	h.markReconciled(c, deviceID)

	ctx := context.Background()
	if err := h.paramsRepo.Upsert(ctx, deviceID, map[string]parameters.CachedValue{
		"Device.WiFi.SSID.1.SSID": {Value: "stale", Source: parameters.SourceGetValues, UpdatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	msg := objectDeletionMsg("sub-od-1", false, "Device.WiFi.SSID.1.")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	cache, err := h.paramsRepo.Get(ctx, deviceID)
	if err != nil {
		t.Fatalf("Get parameter cache: %v", err)
	}
	if _, ok := cache["Device.WiFi.SSID.1.SSID"]; ok {
		t.Errorf("parameter cache = %+v, want Device.WiFi.SSID.1.SSID invalidated", cache)
	}

	events, err := h.devicesRepo.Events(ctx, deviceID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("device events = %+v, want exactly 1", events)
	}
	ev := events[0]
	if ev.ObjPath != "Device.WiFi.SSID.1." || ev.EventName != "ObjectDeletion" {
		t.Errorf("event = %+v, want obj_path=Device.WiFi.SSID.1. event_name=ObjectDeletion", ev)
	}
	if len(ev.Params) != 0 {
		t.Errorf("event params = %+v, want empty for an ObjectDeletion", ev.Params)
	}
	// Fix round 1, Important 2 -- see
	// TestHandlerObjectCreationInvalidatesAndRecords' identical assertion.
	if ev.MsgID != msg.GetHeader().GetMsgId() {
		t.Errorf("event msg_id = %q, want %q (the Notify's own msg_id)", ev.MsgID, msg.GetHeader().GetMsgId())
	}
}

// TestHandlerEventRecordsEvent covers the checklist's Event row: an
// arbitrary Event Notify must be recorded with its own name and params,
// with no parameter-cache involvement.
func TestHandlerEventRecordsEvent(t *testing.T) {
	h, deviceID := newNotifyTestHandler(t)
	c := &captureConn{id: agent}
	h.markReconciled(c, deviceID)

	msg := eventMsg("sub-ev-1", false, "Device.", "Boot!", map[string]string{"Cause": "LocalReboot"})
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	events, err := h.devicesRepo.Events(context.Background(), deviceID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("device events = %+v, want exactly 1", events)
	}
	ev := events[0]
	if ev.ObjPath != "Device." || ev.EventName != "Boot!" {
		t.Errorf("event = %+v, want obj_path=Device. event_name=Boot!", ev)
	}
	if ev.Params["Cause"] != "LocalReboot" {
		t.Errorf("event params = %+v, want Cause=LocalReboot", ev.Params)
	}
	// Fix round 1, Important 2 -- see
	// TestHandlerObjectCreationInvalidatesAndRecords' identical assertion.
	if ev.MsgID != msg.GetHeader().GetMsgId() {
		t.Errorf("event msg_id = %q, want %q (the Notify's own msg_id)", ev.MsgID, msg.GetHeader().GetMsgId())
	}
}

// TestHandlerObjectCreationRecordEventDedupsRedelivery covers fix round
// 1, Important 2(b): USP's Notify delivery is at-least-once, so an agent
// can (and will) redeliver the exact same Notify -- same msg_id -- after
// a dropped ack. Feeding the identical wire record through OnRecord
// twice must still leave exactly one device_events row, proving
// RecordEvent's own ON CONFLICT (device_id, msg_id) DO NOTHING dedup
// actually works end-to-end through this handler, not merely that
// msg_id is passed to it somewhere (which the msg_id assertions above
// check, but wouldn't by themselves catch e.g. RecordEvent being called
// with a freshly-generated id per call).
func TestHandlerObjectCreationRecordEventDedupsRedelivery(t *testing.T) {
	h, deviceID := newNotifyTestHandler(t)
	c := &captureConn{id: agent}
	h.markReconciled(c, deviceID)

	msg := objectCreationMsg("sub-oc-dedup", false, "Device.WiFi.SSID.1.", map[string]string{"Alias": "cpe-ssid-1"})
	wire := recordWire(t, agent, ctrl, msg)

	h.OnRecord(mtp.Inbound{Conn: c, Record: wire})
	h.OnRecord(mtp.Inbound{Conn: c, Record: wire}) // simulated redelivery: identical msg_id

	events, err := h.devicesRepo.Events(context.Background(), deviceID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("device events = %+v, want exactly 1 despite the redelivered Notify sharing the same msg_id", events)
	}
}

// TestHandlerNotifyDroppedWhenUnreconciled covers the checklist's
// unreconciled-connection row for all four Notify types: dropped,
// logged, no writes -- not even a NotifyResp, despite SendResp true.
func TestHandlerNotifyDroppedWhenUnreconciled(t *testing.T) {
	tests := []struct {
		name string
		msg  *uspproto.Msg
	}{
		{"ValueChange", valueChangeMsg("sub-1", true, "Device.WiFi.SSID.1.SSID", "MyNetwork")},
		{"ObjectCreation", objectCreationMsg("sub-1", true, "Device.WiFi.SSID.1.", map[string]string{"Alias": "x"})},
		{"ObjectDeletion", objectDeletionMsg("sub-1", true, "Device.WiFi.SSID.1.")},
		{"Event", eventMsg("sub-1", true, "Device.", "Boot!", map[string]string{"Cause": "LocalReboot"})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, deviceID := newNotifyTestHandler(t)
			c := &captureConn{id: agent}
			// Deliberately not marked reconciled.

			h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, tc.msg)})

			if len(c.sent) != 0 {
				t.Fatalf("c.sent = %d records, want 0: an unreconciled connection's Notify must be dropped entirely, no NotifyResp even with SendResp true", len(c.sent))
			}
			cache, err := h.paramsRepo.Get(context.Background(), deviceID)
			if err != nil {
				t.Fatalf("Get parameter cache: %v", err)
			}
			if len(cache) != 0 {
				t.Errorf("parameter cache = %+v, want empty: a dropped Notify must not write", cache)
			}
			events, err := h.devicesRepo.Events(context.Background(), deviceID, 0)
			if err != nil {
				t.Fatalf("Events: %v", err)
			}
			if len(events) != 0 {
				t.Errorf("device events = %+v, want empty: a dropped Notify must not write", events)
			}
		})
	}
}

// TestHandlerNotifySendsResp covers the checklist's SendResp row for
// each of the four types: SendResp true must produce a NotifyResp
// carrying the Notify's own subscription_id, mirroring
// TestHandlerOnBoardRequestSendsResp/TestHandlerOperationCompleteSendsResp.
// Each subtest seeds a real usp_subscriptions row matching its own
// subscription_id (a valid UUID, per that table's column type) so
// checkSubscriptionID's own known-id path is taken and no reconcile Get
// is interleaved into c.sent alongside the NotifyResp this test is
// actually about (that interleaving is covered separately by
// TestHandlerUnknownSubscriptionIDTriggersReconcile).
func TestHandlerNotifySendsResp(t *testing.T) {
	tests := []struct {
		name      string
		notifType string
		msg       func(subscriptionID string) *uspproto.Msg
	}{
		{"ValueChange", "ValueChange", func(id string) *uspproto.Msg { return valueChangeMsg(id, true, "Device.WiFi.SSID.1.SSID", "MyNetwork") }},
		{"ObjectCreation", "ObjectCreation", func(id string) *uspproto.Msg { return objectCreationMsg(id, true, "Device.WiFi.SSID.1.", nil) }},
		{"ObjectDeletion", "ObjectDeletion", func(id string) *uspproto.Msg { return objectDeletionMsg(id, true, "Device.WiFi.SSID.1.") }},
		{"Event", "Event", func(id string) *uspproto.Msg { return eventMsg(id, true, "Device.", "Boot!", nil) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, deviceID := newNotifyTestHandler(t)
			c := &captureConn{id: agent}
			h.markReconciled(c, deviceID)

			subscriptionID := uuid.New().String()
			if err := h.subscriptions.repo.Create(context.Background(), subscriptions.Subscription{
				ID:         subscriptionID,
				DeviceID:   deviceID,
				NotifType:  tc.notifType,
				Persistent: true,
				CreatedBy:  "test",
				CreatedAt:  time.Now().UTC().Truncate(time.Microsecond),
			}); err != nil {
				t.Fatalf("seed subscription: %v", err)
			}

			h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, tc.msg(subscriptionID))})

			if len(c.sent) != 1 {
				t.Fatalf("c.sent has %d records, want exactly 1 (the NotifyResp)", len(c.sent))
			}
			rec, err := usp.DecodeRecord(c.sent[0], agent)
			if err != nil {
				t.Fatalf("decode NotifyResp record: %v", err)
			}
			respMsg, err := usp.DecodeMsg(rec.Payload)
			if err != nil {
				t.Fatalf("decode NotifyResp msg: %v", err)
			}
			if respMsg.GetHeader().GetMsgType() != uspproto.Header_NOTIFY_RESP {
				t.Fatalf("msg type = %v, want NOTIFY_RESP", respMsg.GetHeader().GetMsgType())
			}
			if got := respMsg.GetBody().GetResponse().GetNotifyResp().GetSubscriptionId(); got != subscriptionID {
				t.Errorf("NotifyResp subscription_id = %q, want %q", got, subscriptionID)
			}
		})
	}
}

// TestHandlerUnknownSubscriptionIDTriggersReconcile covers the
// checklist's unknown-subscription_id row: a ValueChange (representative
// of all four) carrying a subscription_id with no matching
// usp_subscriptions row must still be processed (the parameter cache is
// still updated) AND must trigger a subscription reconciliation pass --
// observed here as the reconciler's own read Get landing on the same
// connection (design S7.3: a bookkeeping drift is not evidence the
// Notify's data itself is wrong).
func TestHandlerUnknownSubscriptionIDTriggersReconcile(t *testing.T) {
	h, deviceID := newNotifyTestHandler(t)
	c := &captureConn{id: agent}
	h.markReconciled(c, deviceID)

	msg := valueChangeMsg("unknown-subscription-id", false, "Device.WiFi.SSID.1.SSID", "MyNetwork")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg)})

	cache, err := h.paramsRepo.Get(context.Background(), deviceID)
	if err != nil {
		t.Fatalf("Get parameter cache: %v", err)
	}
	if cv, ok := cache["Device.WiFi.SSID.1.SSID"]; !ok || cv.Value != "MyNetwork" {
		t.Errorf("parameter cache = %+v, want Device.WiFi.SSID.1.SSID=MyNetwork despite the unknown subscription_id", cache)
	}

	if len(c.sent) != 1 {
		t.Fatalf("c.sent = %d records, want exactly 1 (the subscription reconciler's own read Get)", len(c.sent))
	}
	subscriptionReadMsgID(t, c.sent[0])
}

// TestHandlerUnknownSubscriptionIDDebouncesReconcile covers fix round 1,
// Important 1: before subscriptionReconciler.reconcile's own inFlight
// guard, checkSubscriptionID called reconcile once per Notify with no
// cap -- a device sending several unknown-subscription-id Notifies
// before it ever answers the reconciler's own read Get would grow
// s.pending without bound and re-send the read Get every time, throttling
// the connection's own message processing with an inline ByDevice query
// plus a Get send per Notify. Two such Notifies back to back (no
// response to either in between) must collapse into exactly one reconcile
// pass: one Get sent, one pending entry -- not two.
func TestHandlerUnknownSubscriptionIDDebouncesReconcile(t *testing.T) {
	h, deviceID := newNotifyTestHandler(t)
	c := &captureConn{id: agent}
	h.markReconciled(c, deviceID)

	msg1 := valueChangeMsg("unknown-subscription-id-1", false, "Device.WiFi.SSID.1.SSID", "MyNetwork")
	msg2 := valueChangeMsg("unknown-subscription-id-2", false, "Device.WiFi.SSID.1.SSID", "MyOtherNetwork")
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg1)})
	h.OnRecord(mtp.Inbound{Conn: c, Record: recordWire(t, agent, ctrl, msg2)})

	if len(c.sent) != 1 {
		t.Fatalf("c.sent = %d records, want exactly 1: two unknown-subscription-id Notifies with no response in between must collapse into one reconcile pass, not two", len(c.sent))
	}
	subscriptionReadMsgID(t, c.sent[0])

	h.subscriptions.mu.Lock()
	pendingLen := len(h.subscriptions.pending)
	h.subscriptions.mu.Unlock()
	if pendingLen != 1 {
		t.Errorf("subscriptions.pending has %d entries, want exactly 1 (the one read already in flight)", pendingLen)
	}

	// The second Notify's own data must still have been processed --
	// debouncing the reconcile trigger must not discard the Notify's
	// content (same "log and reconcile, don't drop" contract
	// checkSubscriptionID's own doc comment states).
	cache, err := h.paramsRepo.Get(context.Background(), deviceID)
	if err != nil {
		t.Fatalf("Get parameter cache: %v", err)
	}
	if cv, ok := cache["Device.WiFi.SSID.1.SSID"]; !ok || cv.Value != "MyOtherNetwork" {
		t.Errorf("parameter cache = %+v, want Device.WiFi.SSID.1.SSID=MyOtherNetwork (the second Notify's value)", cache)
	}
}

// TestReconcileTriggersSubscriptionReconcile covers the checklist's
// on-connect row: resolveAndMarkReconciled's success path must trigger
// subscription reconciliation alongside job dispatch (B-3b's own
// tryDispatch trigger), observed as the subscription reconciler's read
// Get landing on the connection. Uses a fakeIdentityStore (like
// dispatcher_test.go's own tryDispatch tests) for reconciler/dispatcher,
// since neither is what this test is about; only h.subscriptions needs a
// real *subscriptions.Repository (reconcile's ByDevice is a plain SELECT
// with no foreign-key dependency on a real devices row, so a device id
// with no matching devices row is fine here -- it must still be
// UUID-shaped, though, since usp_subscriptions.device_id is a UUID
// column and even a plain SELECT's parameter is type-checked against it).
func TestReconcileTriggersSubscriptionReconcile(t *testing.T) {
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed integration test")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	fakeStore := newFakeIdentityStore()
	registry := mtp.NewRegistry()
	c := &captureConn{id: agent}
	deviceID := uuid.New().String()
	registerAgent(fakeStore, registry, deviceID, c)

	h := &handler{
		log:           slog.Default(),
		registry:      registry,
		probe:         newProbe(ctrl, slog.Default()),
		controllerID:  ctrl,
		metrics:       newUSPMetrics(observability.NewMetrics("uspc-test")),
		reconciler:    newReconciler(fakeStore, slog.Default()),
		dispatcher:    newDispatcher(nil, fakeStore, registry, ctrl, slog.Default()),
		subscriptions: newSubscriptionReconciler(subscriptions.NewRepository(db), ctrl, slog.Default()),
	}

	h.resolveAndMarkReconciled(c)

	if len(c.sent) != 1 {
		t.Fatalf("c.sent = %d records, want exactly 1 (the subscription reconciler's own read Get, triggered alongside dispatch)", len(c.sent))
	}
	subscriptionReadMsgID(t, c.sent[0])
}
