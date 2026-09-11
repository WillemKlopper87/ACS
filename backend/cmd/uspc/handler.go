package main

import (
	"context"
	"errors"
	"log/slog"

	"acs/internal/usp"
	"acs/internal/usp/mtp"
)

// handler implements mtp.Handler, wiring transport lifecycle events to
// the connection registry, the interop probe, and metrics/logging. It
// never inspects USP message content itself beyond decoding the Record
// envelope and handing the payload to the probe -- real dispatch is a
// later plan (B-3).
type handler struct {
	log          *slog.Logger
	registry     *mtp.Registry
	probe        *probe
	controllerID usp.EndpointID
	metrics      *uspMetrics
}

var _ mtp.Handler = (*handler)(nil)

// OnConnect registers c, closing whatever connection it replaced (an
// agent that reconnects, or reconnects over a different MTP), then
// kicks off the interop probe.
func (h *handler) OnConnect(c mtp.Conn) {
	h.metrics.connections.WithLabelValues(string(c.Kind())).Inc()

	if replaced := h.registry.Add(c); replaced != nil {
		if err := replaced.Close("replaced by new connection"); err != nil {
			h.log.Warn("uspc: failed to close replaced connection", "endpoint", replaced.Endpoint(), "mtp", replaced.Kind(), "error", err)
		}
	}

	if err := h.probe.start(context.Background(), c); err != nil {
		h.log.Warn("uspc: probe failed to start", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
	}
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
	h.probe.handle(rec.From, msg)
}

// OnDisconnect removes c from the registry and drops any of its
// outstanding probes, so neither leaks past the connection's life.
func (h *handler) OnDisconnect(c mtp.Conn, err error) {
	h.registry.Remove(c)
	h.probe.forget(c)
	h.metrics.connections.WithLabelValues(string(c.Kind())).Dec()

	if err != nil {
		h.log.Info("uspc: connection disconnected", "endpoint", c.Endpoint(), "mtp", c.Kind(), "error", err)
		return
	}
	h.log.Info("uspc: connection disconnected", "endpoint", c.Endpoint(), "mtp", c.Kind())
}
