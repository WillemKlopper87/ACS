package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"
)

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

	// An OnBoardRequest Notify and a probe GetResp are mutually exclusive
	// message shapes for a given msg, so trying the former first and
	// falling through to the probe on ErrNotOnBoardRequest never
	// intercepts probe traffic.
	if ob, err := usp.DecodeOnBoardRequest(msg); err == nil {
		h.handleOnBoardRequest(in.Conn, ob)
		return
	}

	if matched := h.probe.handle(rec.From, msg); matched {
		h.handleProbeFallback(in.Conn, msg)
	}
}

// handleOnBoardRequest reconciles identity from an OnBoardRequest Notify
// (the primary reconciliation path, design spec S5.3) and, if the agent
// requested one (ob.SendResp), sends back a NotifyResp.
func (h *handler) handleOnBoardRequest(c mtp.Conn, ob *usp.OnBoardRequest) {
	ctx := context.Background()
	if err := h.reconciler.onBoard(ctx, c, ob); err != nil {
		h.log.Warn("uspc: failed to reconcile onboard request", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
		return
	}
	h.resolveAndMarkReconciled(ctx, c)

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
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
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
	ctx := context.Background()
	if err := h.reconciler.fromProbeFallback(ctx, c, oui, productClass, serialNumber); err != nil {
		h.log.Warn("uspc: failed to reconcile via probe fallback", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
		return
	}
	h.resolveAndMarkReconciled(ctx, c)
}

// resolveAndMarkReconciled looks up the usp_agents row a just-succeeded
// reconciliation created (or refreshed) for c, so handler can remember
// its device id for OnDisconnect -- reconciler.onBoard/fromProbeFallback
// report only success/failure, not the resolved device, by design (see
// task-5 brief's Produces contract).
func (h *handler) resolveAndMarkReconciled(ctx context.Context, c mtp.Conn) {
	agentRow, err := h.reconciler.store.GetUspAgentByEndpointID(ctx, string(c.Endpoint()))
	if err != nil {
		h.log.Warn("uspc: reconciled connection but failed to resolve its device id", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
		return
	}
	h.markReconciled(c, agentRow.DeviceID)
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
func (h *handler) OnDisconnect(c mtp.Conn, err error) {
	h.registry.Remove(c)
	h.probe.forget(c)
	h.metrics.connections.WithLabelValues(string(c.Kind())).Dec()

	if deviceID, reconciled := h.reconciledDeviceID(c); reconciled {
		h.reconciler.disconnect(context.Background(), deviceID)
	}
	h.forgetIdentity(c)

	if err != nil {
		h.log.Info("uspc: connection disconnected", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
		return
	}
	h.log.Info("uspc: connection disconnected", "endpoint", c.Endpoint(), "mtp", c.Kind())
}
