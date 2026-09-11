package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"acs/internal/devices"
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
	log          *slog.Logger
	registry     *mtp.Registry
	probe        *probe
	controllerID usp.EndpointID
	metrics      *uspMetrics
	reconciler   *reconciler
	dispatcher   *dispatcher

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

	if matched := h.probe.handle(rec.From, msg); matched {
		h.handleProbeFallback(in.Conn, msg)
		return
	}

	if h.dispatcher != nil {
		h.dispatcher.handleResponse(rec.From, msg)
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
	payload, err := usp.EncodeNotifyResp(usp.NewMsgID(), ob.SubscriptionID)
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
	payload, err := usp.EncodeNotifyResp(usp.NewMsgID(), oc.SubscriptionID)
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
