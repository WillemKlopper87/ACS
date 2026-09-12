package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"
)

// probeParamPath is the one path every USP agent is expected to answer,
// regardless of vendor data model: interop evidence that a fresh
// connection actually speaks USP, not a data-model exploration.
const probeParamPath = "Device.DeviceInfo."

// sendTimeout bounds how long start waits for c.Send to accept the
// probe Get. Without a deadline here, a wedged socket would block
// Handler.OnConnect (and therefore the transport's accept/read loop
// that called it) indefinitely.
const sendTimeout = 5 * time.Second

// pendingProbe is what start remembers about one outstanding probe Get,
// keyed by its msg_id, so handle can log which MTP a late response
// belongs to and forget can tell which connection it was sent on.
type pendingProbe struct {
	conn mtp.Conn
}

// probe sends a Get(["Device.DeviceInfo."]) to every newly connected
// agent and logs the answer -- the interop evidence Task 6's CI job
// asserts on, and a later plan replaces with real dispatch (B-3).
//
// State is a mutex-guarded map from msg_id to the connection the Get was
// sent on. An entry is removed the moment it matches a response or
// error, so a duplicate/late reply cannot match twice; unmatched entries
// are dropped by forget when their connection disconnects, so a probe
// that never gets an answer does not leak forever.
type probe struct {
	controllerID usp.EndpointID
	log          *slog.Logger
	// metrics is optional -- nil in probe_test.go's unit tests, which
	// construct a probe directly via newProbe and never call setMetrics.
	// start and handle skip recording when it is nil.
	metrics *uspMetrics

	mu      sync.Mutex
	pending map[string]pendingProbe
}

// newProbe returns a probe ready for concurrent use. log defaults to
// slog.Default() when nil, matching this codebase's other constructors.
func newProbe(controllerID usp.EndpointID, log *slog.Logger) *probe {
	if log == nil {
		log = slog.Default()
	}
	return &probe{
		controllerID: controllerID,
		log:          log,
		pending:      make(map[string]pendingProbe),
	}
}

// setMetrics wires m into the probe, so start can count its outbound Get
// under acs_usp_records_total{direction="out"}. Separate from newProbe
// (rather than a constructor parameter) because probe_test.go's unit
// tests construct a probe with no metrics at all.
func (p *probe) setMetrics(m *uspMetrics) {
	p.metrics = m
}

// recordOut increments acs_usp_records_total{mtp,direction="out",result}
// if metrics are wired, a no-op otherwise. result uses the same
// vocabulary OnRecord uses for inbound records (ok/decode_error/...);
// an encode or send failure -- there being no data to decode, only to
// produce -- is counted as decode_error, the closest existing bucket,
// rather than growing the label's cardinality with an outbound-only
// value the brief's metric contract does not name.
func (p *probe) recordOut(kind mtp.Kind, result string) {
	if p.metrics == nil {
		return
	}
	p.metrics.records.WithLabelValues(string(kind), "out", result).Inc()
}

// start sends the probe Get on c and remembers its msg_id. Called from
// Handler.OnConnect, before any record has been read from c.
func (p *probe) start(ctx context.Context, c mtp.Conn) error {
	msgID := usp.NewMsgID()

	payload, err := usp.EncodeGet(msgID, []string{probeParamPath}, 1)
	if err != nil {
		p.recordOut(c.Kind(), "decode_error")
		return fmt.Errorf("probe: encode Get: %w", err)
	}
	record, err := usp.EncodeRecord(p.controllerID, c.Endpoint(), payload)
	if err != nil {
		p.recordOut(c.Kind(), "decode_error")
		return fmt.Errorf("probe: encode record: %w", err)
	}

	p.mu.Lock()
	p.pending[msgID] = pendingProbe{conn: c}
	p.mu.Unlock()

	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := c.Send(sendCtx, record); err != nil {
		p.mu.Lock()
		delete(p.pending, msgID)
		p.mu.Unlock()
		p.recordOut(c.Kind(), "decode_error")
		return fmt.Errorf("probe: send Get: %w", err)
	}
	p.recordOut(c.Kind(), "ok")
	return nil
}

// handle reports whether msg answers one of this probe's outstanding
// Gets (by msg_id), logging the result either way it can be
// interpreted: a GetResp's resolved parameters, or an Error body's
// USPError. from is the endpoint the record carrying msg arrived from,
// used only for logging -- matching is by msg_id alone, per pending's
// key.
//
// A nil msg, a nil header, or an unrecognised msg_id is handled without
// panicking: this is called on every inbound message, most of which
// have nothing to do with the probe.
func (p *probe) handle(from usp.EndpointID, msg *uspproto.Msg) (matched bool) {
	if msg == nil || msg.GetHeader() == nil {
		return false
	}

	msgID := msg.GetHeader().GetMsgId()
	p.mu.Lock()
	entry, ok := p.pending[msgID]
	if ok {
		delete(p.pending, msgID)
	}
	p.mu.Unlock()
	if !ok {
		return false
	}
	kind := entry.conn.Kind()

	if uspErr := usp.ErrorFromMsg(msg); uspErr != nil {
		p.log.Warn("usp probe: agent returned an error", "endpoint", from, "mtp", kind, "msg_id", msgID, "error", uspErr)
		return true
	}

	getResp := msg.GetBody().GetResponse().GetGetResp()
	if getResp == nil {
		p.log.Warn("usp probe: response to probe Get was not a GetResp", "endpoint", from, "mtp", kind, "msg_id", msgID, "msg_type", msg.GetHeader().GetMsgType())
		return true
	}

	for _, reqResult := range getResp.GetReqPathResults() {
		for _, resolved := range reqResult.GetResolvedPathResults() {
			for name, value := range resolved.GetResultParams() {
				p.log.Info("usp probe: parameter", "endpoint", from, "mtp", kind,
					"param_path", resolved.GetResolvedPath()+name, "value", value)
			}
		}
	}
	return true
}

// forget drops every pending probe sent on c. Called from
// Handler.OnDisconnect with the disconnecting Conn itself, not its
// endpoint id: an agent's reconnect can register a new Conn for the same
// endpoint id before the old Conn's disconnect callback lands (the same
// takeover race mtp.Registry.Remove guards against), and keying on the
// endpoint id alone would let a stale disconnect delete the new
// connection's just-registered probe out from under it.
func (p *probe) forget(c mtp.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, entry := range p.pending {
		if entry.conn == c {
			delete(p.pending, id)
		}
	}
}
