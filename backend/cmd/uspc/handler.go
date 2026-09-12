package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"acs/internal/devices"
	"acs/internal/parameters"
	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"
)

// dbCallTimeout bounds every identity-reconciliation call this handler
// makes into internal/devices (via reconciler/identityStore). OnRecord
// and OnDisconnect run inline on a transport's own read/lifecycle
// goroutine (wsConn.readLoop; the MQTT broker's per-client goroutine), so
// a wedged Postgres must not be allowed to stall that connection's own
// reads -- the same hazard probe.go's sendTimeout already documents and
// guards against for the Send path.
const dbCallTimeout = 5 * time.Second

// connIdentity is the per-connection state handler tracks for identity
// reconciliation: whether this connection has been reconciled to a
// devices row yet (via OnBoardRequest or, failing that, the probe
// fallback) and, if so, which device it resolved to -- needed so
// OnDisconnect knows whether/what to mark disconnected, and so the
// probe-fallback path can stay quiet once OnBoardRequest already did the
// job (Correction 4, task-5 brief).
type connIdentity struct {
	deviceID   string
	reconciled bool
}

// handler implements mtp.Handler, wiring transport lifecycle events to
// the connection registry, the interop probe, identity reconciliation,
// and metrics/logging. Message content is otherwise not inspected
// beyond decoding the Record envelope, an OnBoardRequest Notify (this
// task), and handing the payload to the probe -- real dispatch is a
// later plan (B-3).
type handler struct {
	log           *slog.Logger
	registry      *mtp.Registry
	probe         *probe
	controllerID  usp.EndpointID
	metrics       *uspMetrics
	reconciler    *reconciler
	dispatcher    *dispatcher
	subscriptions *subscriptionReconciler
	paramsRepo    *parameters.Repository
	devicesRepo   *devices.Repository

	// identMu guards identities. This is handler's own lock, deliberately
	// separate from mtp.Registry's internal one -- that lock guards
	// Registry's own state, not connIdentity, which belongs to handler.
	identMu    sync.Mutex
	identities map[mtp.Conn]*connIdentity
}

var _ mtp.Handler = (*handler)(nil)

// OnConnect registers c, closing whatever connection it replaced (an
// agent that reconnects, or reconnects over a different MTP), then
// kicks off the interop probe.
func (h *handler) OnConnect(c mtp.Conn) {
	h.metrics.connections.WithLabelValues(string(c.Kind())).Inc()

	h.identMu.Lock()
	if h.identities == nil {
		h.identities = make(map[mtp.Conn]*connIdentity)
	}
	h.identities[c] = &connIdentity{}
	h.identMu.Unlock()

	if replaced := h.registry.Add(c); replaced != nil {
		if err := replaced.Close("replaced by new connection"); err != nil {
			h.log.Warn("uspc: failed to close replaced connection", "endpoint", replaced.Endpoint(), "mtp", replaced.Kind(), "error", err)
		}
	}

	if err := h.probe.start(context.Background(), c); err != nil {
		h.log.Warn("uspc: probe failed to start", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
	}
}

// isReconciled reports whether c has already been reconciled to a
// device, by either path.
func (h *handler) isReconciled(c mtp.Conn) bool {
	h.identMu.Lock()
	defer h.identMu.Unlock()
	ci := h.identities[c]
	return ci != nil && ci.reconciled
}

// reconciledDeviceID returns the device id c was reconciled to, if any.
func (h *handler) reconciledDeviceID(c mtp.Conn) (string, bool) {
	h.identMu.Lock()
	defer h.identMu.Unlock()
	ci := h.identities[c]
	if ci == nil || !ci.reconciled {
		return "", false
	}
	return ci.deviceID, true
}

// markReconciled records that c resolved to deviceID, via whichever path
// (OnBoardRequest or probe fallback) got there first.
func (h *handler) markReconciled(c mtp.Conn, deviceID string) {
	h.identMu.Lock()
	defer h.identMu.Unlock()
	if h.identities == nil {
		h.identities = make(map[mtp.Conn]*connIdentity)
	}
	h.identities[c] = &connIdentity{deviceID: deviceID, reconciled: true}
}

// forgetIdentity drops c's per-connection identity state, mirroring the
// memory-leak discipline probe.forget/mtp.Registry.Remove already follow
// for their own per-connection state.
func (h *handler) forgetIdentity(c mtp.Conn) {
	h.identMu.Lock()
	defer h.identMu.Unlock()
	delete(h.identities, c)
}

// OnRecord decodes the inbound Record and, for a well-formed message,
// hands it to the probe's response matcher. Per R-WS.16, a WebSocket
// connection that sends an undecodable record is closed outright; MQTT
// has no equivalent transport-level close-on-protocol-violation, so a
// bad record there is only logged and dropped.
func (h *handler) OnRecord(in mtp.Inbound) {
	kind := string(in.Conn.Kind())
	rec, err := usp.DecodeRecord(in.Record, h.controllerID)

	switch {
	case err == nil:
		// "ok" is recorded below, only once DecodeMsg has also succeeded:
		// a well-formed Record envelope wrapping an undecodable message
		// payload is not actually an "ok" record.
	case errors.Is(err, usp.ErrNoPayload):
		h.metrics.records.WithLabelValues(kind, "in", "no_payload").Inc()
		h.log.Info("uspc: record carries no message payload", "endpoint", in.Conn.Endpoint(), "mtp", kind,
			"record_type", rec.Type, "disconnect_reason", rec.DisconnectReason)
		return
	case errors.Is(err, usp.ErrSessionContextUnsupported):
		h.metrics.records.WithLabelValues(kind, "in", "unsupported").Inc()
		h.log.Warn("uspc: session context records are not supported", "endpoint", in.Conn.Endpoint(), "mtp", kind)
		return
	default:
		h.metrics.records.WithLabelValues(kind, "in", "decode_error").Inc()
		h.log.Warn("uspc: failed to decode record", "endpoint", in.Conn.Endpoint(), "mtp", kind, "error", err)
		if in.Conn.Kind() == mtp.KindWebSocket {
			if closeErr := in.Conn.Close("undecodable record"); closeErr != nil {
				h.log.Warn("uspc: failed to close connection after undecodable record", "endpoint", in.Conn.Endpoint(), "error", closeErr)
			}
		}
		return
	}

	msg, err := usp.DecodeMsg(rec.Payload)
	if err != nil {
		h.metrics.records.WithLabelValues(kind, "in", "decode_error").Inc()
		h.log.Warn("uspc: failed to decode message payload", "endpoint", in.Conn.Endpoint(), "mtp", kind, "error", err)
		return
	}
	h.metrics.records.WithLabelValues(kind, "in", "ok").Inc()

	// An OnBoardRequest Notify and an OperationComplete Notify are both
	// Notify-shaped messages, structurally distinct from the probe's
	// GetResp and a dispatched job's *Resp -- so both are tried first
	// (only one wasted decode attempt for a non-Notify message, zero for
	// a well-formed one), before falling through to the probe/dispatch
	// checks that a Notify will never match anyway.
	if ob, err := usp.DecodeOnBoardRequest(msg); err == nil {
		h.handleOnBoardRequest(in.Conn, ob)
		return
	}

	if oc, err := usp.DecodeOperationComplete(msg); err == nil {
		h.handleOperationComplete(in.Conn, oc)
		return
	}

	// ValueChange/ObjectCreation/ObjectDeletion/Event are the remaining
	// four Notify variants -- same "tried first" reasoning as the two
	// above, and mutually exclusive with them and each other by
	// construction. msg.GetHeader().GetMsgId() is threaded through to
	// handleObjectCreation/handleObjectDeletion/handleEvent (not
	// handleValueChange, which needs no msg_id) because
	// devices.Repository.RecordEvent's own redelivery dedup is keyed on
	// exactly that id, and it is not part of the decoded
	// ObjectCreation/ObjectDeletion/Event structs themselves.
	if vc, err := usp.DecodeValueChange(msg); err == nil {
		h.handleValueChange(in.Conn, vc)
		return
	}
	if oc, err := usp.DecodeObjectCreation(msg); err == nil {
		h.handleObjectCreation(in.Conn, oc, msg.GetHeader().GetMsgId())
		return
	}
	if od, err := usp.DecodeObjectDeletion(msg); err == nil {
		h.handleObjectDeletion(in.Conn, od, msg.GetHeader().GetMsgId())
		return
	}
	if ev, err := usp.DecodeEvent(msg); err == nil {
		h.handleEvent(in.Conn, ev, msg.GetHeader().GetMsgId())
		return
	}

	if matched := h.probe.handle(rec.From, msg); matched {
		h.handleProbeFallback(in.Conn, msg)
		return
	}

	if h.dispatcher != nil {
		if matched := h.dispatcher.handleResponse(rec.From, msg); matched {
			return
		}
	}

	// The subscription reconciler's own AddResp/DeleteResp/GetResp
	// answers are *Resp-shaped, same family as the probe/dispatcher
	// checks above, but correlated against subscriptionReconciler's own
	// separate pending map (its requests were never registered with
	// dispatcher), so they need their own fallthrough link, tried last.
	if h.subscriptions != nil {
		h.subscriptions.handleResponse(rec.From, msg)
	}
}

// handleOnBoardRequest reconciles identity from an OnBoardRequest Notify
// (the primary reconciliation path, design spec S5.3) and, if the agent
// requested one (ob.SendResp), sends back a NotifyResp.
func (h *handler) handleOnBoardRequest(c mtp.Conn, ob *usp.OnBoardRequest) {
	onboardCtx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
	err := h.reconciler.onBoard(onboardCtx, c, ob)
	cancel()
	if err != nil {
		h.logReconcileFailure(c, "failed to reconcile onboard request", err)
		return
	}
	h.resolveAndMarkReconciled(c)

	if !ob.SendResp {
		return
	}
	h.sendNotifyResp(c, ob.SubscriptionID)
}

// handleOperationComplete resolves the job an OperationComplete Notify
// names via oc.CommandKey (async completion, design S6.3), bound to this
// connection's own reconciled device id -- see
// dispatcher.handleOperationComplete's own doc comment for the identity
// check itself. c's reconciled device id is looked up here rather than
// passed in by OnRecord because an unreconciled connection correctly
// yields "" (reconciledDeviceID's own ok bool doesn't matter), which
// dispatcher.handleOperationComplete's identity check already treats as
// a refusal -- no special-casing needed at this call site. If the agent
// requested one (oc.SendResp), sends back a NotifyResp, exactly mirroring
// handleOnBoardRequest's own SendResp handling.
func (h *handler) handleOperationComplete(c mtp.Conn, oc *usp.OperationComplete) {
	connDeviceID, _ := h.reconciledDeviceID(c)

	if h.dispatcher != nil {
		resolveCtx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
		err := h.dispatcher.handleOperationComplete(resolveCtx, connDeviceID, oc)
		cancel()
		if err != nil {
			h.log.Warn("uspc: failed to handle OperationComplete", "endpoint", c.Endpoint(), "mtp", c.Kind(), "command_key", oc.CommandKey, "error", err)
		}
	}

	if !oc.SendResp {
		return
	}
	h.sendNotifyResp(c, oc.SubscriptionID)
}

// sendNotifyResp encodes and sends a NotifyResp acknowledging
// subscriptionID, exactly the shape handleOnBoardRequest/
// handleOperationComplete originally each built inline. Extracted once
// both of them (plus this task's four new Notify handlers) needed the
// identical payload/record/send sequence, so it now has exactly one
// implementation instead of six. Callers are responsible for their own
// SendResp check before calling this -- it always sends, unconditionally
// -- and for logging/handling anything beyond a failed send: like the
// inline code it replaces, this only logs on failure and never
// propagates an error to its caller.
func (h *handler) sendNotifyResp(c mtp.Conn, subscriptionID string) {
	payload, err := usp.EncodeNotifyResp(usp.NewMsgID(), subscriptionID)
	if err != nil {
		h.log.Warn("uspc: failed to encode NotifyResp", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
		return
	}
	record, err := usp.EncodeRecord(h.controllerID, c.Endpoint(), payload)
	if err != nil {
		h.log.Warn("uspc: failed to encode NotifyResp record", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
		return
	}
	sendCtx, sendCancel := context.WithTimeout(context.Background(), sendTimeout)
	defer sendCancel()
	if err := c.Send(sendCtx, record); err != nil {
		h.log.Warn("uspc: failed to send NotifyResp", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
	}
}

// checkSubscriptionID validates subscriptionID against deviceID's
// desired-state subscriptions (usp_subscriptions, via the subscription
// reconciler's own repository) for each of the four Notify handlers
// below. A subscription_id with no matching desired row means ACS's own
// bookkeeping has drifted from what's actually on the device -- not that
// the Notify's data is wrong -- so this only logs at Info and triggers a
// fresh subscriptionReconciler.reconcile pass for deviceID (design S7.3,
// the same reconcile a fresh connect would run); it never causes the
// caller to drop the Notify's content. h.subscriptions.repo (an
// unexported field of a sibling type in this same package) is used
// directly rather than adding a new lookup method to
// subscriptions.Repository, to stay within this task's file boundary
// (handler.go/main.go only) -- ByDevice plus a Go-side membership check
// is enough.
//
// A failure to even look up the desired set (a transient DB error) is
// logged and swallowed without triggering reconcile: an unknown
// subscription_id is only actionable when the lookup itself succeeded
// and genuinely found no match.
func (h *handler) checkSubscriptionID(c mtp.Conn, deviceID, subscriptionID string) {
	if h.subscriptions == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
	subs, err := h.subscriptions.repo.ByDevice(ctx, deviceID)
	cancel()
	if err != nil {
		h.log.Warn("uspc: failed to look up desired subscriptions to validate a Notify's subscription_id", "endpoint", c.Endpoint(), "mtp", c.Kind(), "device_id", deviceID, "error", err)
		return
	}
	for _, sub := range subs {
		if sub.ID == subscriptionID {
			return
		}
	}

	h.log.Info("uspc: Notify carries an unknown subscription_id, triggering subscription reconciliation",
		"endpoint", c.Endpoint(), "mtp", c.Kind(), "device_id", deviceID, "subscription_id", subscriptionID)
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), dbCallTimeout)
	defer reconcileCancel()
	if err := h.subscriptions.reconcile(reconcileCtx, deviceID, c); err != nil {
		h.log.Warn("uspc: failed to trigger subscription reconciliation for an unknown subscription_id", "endpoint", c.Endpoint(), "mtp", c.Kind(), "device_id", deviceID, "error", err)
	}
}

// handleValueChange updates the parameter cache from a ValueChange
// Notify (design S7.1). An unreconciled connection is logged and
// dropped -- there is no device id to attribute the value to -- and an
// unknown subscription_id is handled per checkSubscriptionID's own
// contract (logged, reconciled, but the value is still cached: a
// bookkeeping drift is not evidence the reported value itself is wrong).
func (h *handler) handleValueChange(c mtp.Conn, vc *usp.ValueChange) {
	deviceID, ok := h.reconciledDeviceID(c)
	if !ok {
		h.log.Warn("uspc: ValueChange from an unreconciled connection, dropping", "endpoint", c.Endpoint(), "mtp", c.Kind(), "subscription_id", vc.SubscriptionID)
		return
	}
	h.checkSubscriptionID(c, deviceID, vc.SubscriptionID)

	if h.paramsRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
		err := h.paramsRepo.Upsert(ctx, deviceID, map[string]parameters.CachedValue{
			vc.ParamPath: {Value: vc.ParamValue, Source: parameters.SourceUSPNotify, UpdatedAt: time.Now()},
		})
		cancel()
		if err != nil {
			h.log.Warn("uspc: failed to upsert ValueChange into the parameter cache", "endpoint", c.Endpoint(), "mtp", c.Kind(), "device_id", deviceID, "param_path", vc.ParamPath, "error", err)
		}
	}

	if !vc.SendResp {
		return
	}
	h.sendNotifyResp(c, vc.SubscriptionID)
}

// handleObjectCreation invalidates the affected parameter-cache subtree
// and records the event from an ObjectCreation Notify (design S7.1).
// msgID is msg.GetHeader().GetMsgId() from OnRecord's own decode -- see
// OnRecord's own comment for why it can't come from the decoded
// ObjectCreation itself. Identity/subscription-id handling mirrors
// handleValueChange exactly.
func (h *handler) handleObjectCreation(c mtp.Conn, oc *usp.ObjectCreation, msgID string) {
	deviceID, ok := h.reconciledDeviceID(c)
	if !ok {
		h.log.Warn("uspc: ObjectCreation from an unreconciled connection, dropping", "endpoint", c.Endpoint(), "mtp", c.Kind(), "subscription_id", oc.SubscriptionID)
		return
	}
	h.checkSubscriptionID(c, deviceID, oc.SubscriptionID)

	if h.paramsRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
		err := h.paramsRepo.InvalidateSubtree(ctx, deviceID, oc.ObjPath)
		cancel()
		if err != nil {
			h.log.Warn("uspc: failed to invalidate parameter cache subtree after ObjectCreation", "endpoint", c.Endpoint(), "mtp", c.Kind(), "device_id", deviceID, "obj_path", oc.ObjPath, "error", err)
		}
	}
	if h.devicesRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
		err := h.devicesRepo.RecordEvent(ctx, deviceID, msgID, oc.ObjPath, "ObjectCreation", oc.UniqueKeys)
		cancel()
		if err != nil {
			h.log.Warn("uspc: failed to record ObjectCreation event", "endpoint", c.Endpoint(), "mtp", c.Kind(), "device_id", deviceID, "obj_path", oc.ObjPath, "error", err)
		}
	}

	if !oc.SendResp {
		return
	}
	h.sendNotifyResp(c, oc.SubscriptionID)
}

// handleObjectDeletion is handleObjectCreation's ObjectDeletion
// counterpart: same subtree invalidation and event recording, but with
// no UniqueKeys (an ObjectDeletion Notify carries none -- the instance
// is simply gone).
func (h *handler) handleObjectDeletion(c mtp.Conn, od *usp.ObjectDeletion, msgID string) {
	deviceID, ok := h.reconciledDeviceID(c)
	if !ok {
		h.log.Warn("uspc: ObjectDeletion from an unreconciled connection, dropping", "endpoint", c.Endpoint(), "mtp", c.Kind(), "subscription_id", od.SubscriptionID)
		return
	}
	h.checkSubscriptionID(c, deviceID, od.SubscriptionID)

	if h.paramsRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
		err := h.paramsRepo.InvalidateSubtree(ctx, deviceID, od.ObjPath)
		cancel()
		if err != nil {
			h.log.Warn("uspc: failed to invalidate parameter cache subtree after ObjectDeletion", "endpoint", c.Endpoint(), "mtp", c.Kind(), "device_id", deviceID, "obj_path", od.ObjPath, "error", err)
		}
	}
	if h.devicesRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
		err := h.devicesRepo.RecordEvent(ctx, deviceID, msgID, od.ObjPath, "ObjectDeletion", nil)
		cancel()
		if err != nil {
			h.log.Warn("uspc: failed to record ObjectDeletion event", "endpoint", c.Endpoint(), "mtp", c.Kind(), "device_id", deviceID, "obj_path", od.ObjPath, "error", err)
		}
	}

	if !od.SendResp {
		return
	}
	h.sendNotifyResp(c, od.SubscriptionID)
}

// handleEvent records the event from an Event Notify (design S7.1) --
// no parameter cache involvement, since an arbitrary TR-369 event (e.g.
// Boot!, Device.LocalAgent.Subscription.1.ObjectCreation!) doesn't
// necessarily correspond to a parameter value at all. Identity/
// subscription-id/msgID handling mirrors handleObjectCreation.
func (h *handler) handleEvent(c mtp.Conn, ev *usp.Event, msgID string) {
	deviceID, ok := h.reconciledDeviceID(c)
	if !ok {
		h.log.Warn("uspc: Event from an unreconciled connection, dropping", "endpoint", c.Endpoint(), "mtp", c.Kind(), "subscription_id", ev.SubscriptionID)
		return
	}
	h.checkSubscriptionID(c, deviceID, ev.SubscriptionID)

	if h.devicesRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
		err := h.devicesRepo.RecordEvent(ctx, deviceID, msgID, ev.ObjPath, ev.EventName, ev.Params)
		cancel()
		if err != nil {
			h.log.Warn("uspc: failed to record Event", "endpoint", c.Endpoint(), "mtp", c.Kind(), "device_id", deviceID, "obj_path", ev.ObjPath, "event_name", ev.EventName, "error", err)
		}
	}

	if !ev.SendResp {
		return
	}
	h.sendNotifyResp(c, ev.SubscriptionID)
}

// handleProbeFallback is the design spec's "last resort" identity path
// (S5.3): when a matched probe GetResp carries the three TR-181
// Device.DeviceInfo. identity parameters and this connection has not
// already been reconciled via OnBoardRequest (or an earlier GetResp),
// reconcile from them. probe.go itself does not expose resolved
// parameters to its caller (it only logs them), so this does its own
// walk of the same GetResp structure -- see Correction 3, task-5 brief.
func (h *handler) handleProbeFallback(c mtp.Conn, msg *uspproto.Msg) {
	if h.isReconciled(c) {
		return
	}
	oui, productClass, serialNumber, ok := deviceInfoFromGetResp(msg)
	if !ok {
		return
	}
	fallbackCtx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
	err := h.reconciler.fromProbeFallback(fallbackCtx, c, oui, productClass, serialNumber)
	cancel()
	if err != nil {
		h.logReconcileFailure(c, "failed to reconcile via probe fallback", err)
		return
	}
	h.resolveAndMarkReconciled(c)
}

// logReconcileFailure logs a failed identity reconciliation (from either
// handleOnBoardRequest or handleProbeFallback). devices.ErrEndpointIDInUse
// is logged at Error, not Warn, and with wording that says so explicitly:
// it means this connection's endpoint id is already durably bound to a
// different device in usp_agents, which is not a transient condition --
// the agent will retry the same OnBoardRequest forever without an
// operator resolving the collision (final-review finding 6). Every other
// failure (a transient DB error, etc.) keeps the existing Warn treatment,
// since a retry may well succeed on its own next time.
func (h *handler) logReconcileFailure(c mtp.Conn, msg string, err error) {
	if errors.Is(err, devices.ErrEndpointIDInUse) {
		h.log.Error("uspc: "+msg+": endpoint id already linked to a different device -- requires operator intervention, agent will retry indefinitely",
			"endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
		return
	}
	h.log.Warn("uspc: "+msg, "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
}

// resolveAndMarkReconciled looks up the usp_agents row a just-succeeded
// reconciliation created (or refreshed) for c, so handler can remember
// its device id for OnDisconnect -- reconciler.onBoard/fromProbeFallback
// report only success/failure, not the resolved device, by design (see
// task-5 brief's Produces contract). Bounds its own DB call with
// dbCallTimeout, independent of whatever context (if any) the caller was
// working under.
//
// On success it also triggers dispatcher.tryDispatch for the
// newly-known device id -- the "job queued before device connected"
// trigger path (design S6.1). That call is given a fresh
// context.Background(), not ctx: ctx is bound by this function's own
// dbCallTimeout and is about to be canceled by the defer above. But it is
// NOT given an unbounded context either (fix round 1, Important 3):
// OnRecord/resolveAndMarkReconciled run inline on the transport's own
// read-loop goroutine (dbCallTimeout's own doc comment names this exact
// hazard), and tryDispatch chains up to three independently-timed calls
// (a lookup, a lease, a send) that could otherwise block that goroutine
// for their combined worst case rather than a single known budget. A
// single dbCallTimeout wrapped around the whole call caps it at one bound
// instead. A dispatch-trigger failure is logged at Warn, not treated as a
// reconciliation failure: the reconciliation itself already succeeded.
func (h *handler) resolveAndMarkReconciled(c mtp.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
	defer cancel()
	agentRow, err := h.reconciler.store.GetUspAgentByEndpointID(ctx, string(c.Endpoint()))
	if err != nil {
		h.log.Warn("uspc: reconciled connection but failed to resolve its device id", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
		return
	}
	h.markReconciled(c, agentRow.DeviceID)

	dispatchCtx, dispatchCancel := context.WithTimeout(context.Background(), dbCallTimeout)
	defer dispatchCancel()
	if err := h.dispatcher.tryDispatch(dispatchCtx, agentRow.DeviceID); err != nil {
		h.log.Warn("uspc: reconciled connection but failed to trigger dispatch for its device", "endpoint", c.Endpoint(), "mtp", c.Kind(), "device_id", agentRow.DeviceID, "error", err)
	}

	// Same fresh-context-per-call discipline as the dispatch trigger
	// above, its own separate budget: a subscription-reconcile failure
	// must not starve (or be starved by) tryDispatch's own call, and vice
	// versa. h.subscriptions is nil in some test-construction paths
	// (newTestHandler does not set it), matching h.dispatcher's own
	// nil-guard convention elsewhere in this file.
	if h.subscriptions != nil {
		subsCtx, subsCancel := context.WithTimeout(context.Background(), dbCallTimeout)
		defer subsCancel()
		if err := h.subscriptions.reconcile(subsCtx, agentRow.DeviceID, c); err != nil {
			h.log.Warn("uspc: reconciled connection but failed to trigger subscription reconciliation for its device", "endpoint", c.Endpoint(), "mtp", c.Kind(), "device_id", agentRow.DeviceID, "error", err)
		}
	}
}

// deviceInfoFromGetResp walks a GetResp's resolved parameters looking
// for the three TR-181 Issue 2 Device.DeviceInfo. parameters the probe
// fallback path needs to reconcile identity: ManufacturerOUI,
// ProductClass, and SerialNumber. ok is true only if all three were
// found (not necessarily under the same resolved path entry -- the walk
// covers every reqResult/resolved entry in the response).
func deviceInfoFromGetResp(msg *uspproto.Msg) (oui, productClass, serialNumber string, ok bool) {
	getResp := msg.GetBody().GetResponse().GetGetResp()
	if getResp == nil {
		return "", "", "", false
	}

	var haveOUI, haveProductClass, haveSerialNumber bool
	for _, reqResult := range getResp.GetReqPathResults() {
		for _, resolved := range reqResult.GetResolvedPathResults() {
			for name, value := range resolved.GetResultParams() {
				switch resolved.GetResolvedPath() + name {
				case "Device.DeviceInfo.ManufacturerOUI":
					oui, haveOUI = value, true
				case "Device.DeviceInfo.ProductClass":
					productClass, haveProductClass = value, true
				case "Device.DeviceInfo.SerialNumber":
					serialNumber, haveSerialNumber = value, true
				}
			}
		}
	}
	return oui, productClass, serialNumber, haveOUI && haveProductClass && haveSerialNumber
}

// OnDisconnect removes c from the registry and drops any of its
// outstanding probes, so neither leaks past the connection's life.
//
// registry.Remove reports false when c had already been superseded by a
// newer connection for the same endpoint id (a reconnect takeover): the
// registry itself guards against a slow/stale disconnect evicting the
// replacement (see Registry.Remove's doc comment), and identity
// reconciliation must honour the same guard -- otherwise a takeover's
// old connection's own (delayed) OnDisconnect would call
// reconciler.disconnect for a device id the new connection just
// re-linked, permanently marking a still-connected agent as
// disconnected. c's own per-connection identity state is still dropped
// either way, since it belongs to c specifically, not to whichever conn
// currently holds the endpoint id.
func (h *handler) OnDisconnect(c mtp.Conn, err error) {
	removed := h.registry.Remove(c)
	h.probe.forget(c)
	if h.dispatcher != nil {
		h.dispatcher.forget(c)
	}
	if h.subscriptions != nil {
		h.subscriptions.forget(c)
	}
	h.metrics.connections.WithLabelValues(string(c.Kind())).Dec()

	if removed {
		if deviceID, reconciled := h.reconciledDeviceID(c); reconciled {
			disconnectCtx, cancel := context.WithTimeout(context.Background(), dbCallTimeout)
			h.reconciler.disconnect(disconnectCtx, deviceID, string(c.Endpoint()))
			cancel()
		}
	}
	h.forgetIdentity(c)

	if err != nil {
		h.log.Info("uspc: connection disconnected", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
		return
	}
	h.log.Info("uspc: connection disconnected", "endpoint", c.Endpoint(), "mtp", c.Kind())
}
