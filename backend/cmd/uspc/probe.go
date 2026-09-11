package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"acs/internal/usp"
	"acs/internal/usp/mtp"
	"acs/internal/usp/uspproto"
)

// probeParamPath is the one path every USP agent is expected to answer,
// regardless of vendor data model: interop evidence that a fresh
// connection actually speaks USP, not a data-model exploration.
const probeParamPath = "Device.DeviceInfo."

// pendingProbe is what start remembers about one outstanding probe Get,
// keyed by its msg_id, so handle can log which agent and which MTP a
// late response belongs to.
type pendingProbe struct {
	endpoint usp.EndpointID
	kind     mtp.Kind
}

// probe sends a Get(["Device.DeviceInfo."]) to every newly connected
// agent and logs the answer -- the interop evidence Task 6's CI job
// asserts on, and a later plan replaces with real dispatch (B-3).
//
// State is a mutex-guarded map from msg_id to the endpoint (and MTP) the
// Get was sent to. An entry is removed the moment it matches a response
// or error, so a duplicate/late reply cannot match twice; unmatched
// entries are dropped by forget when their connection disconnects, so a
// probe that never gets an answer does not leak forever.
type probe struct {
	controllerID usp.EndpointID
	log          *slog.Logger

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

// start sends the probe Get on c and remembers its msg_id. Called from
// Handler.OnConnect, before any record has been read from c.
func (p *probe) start(ctx context.Context, c mtp.Conn) error {
	msgID := usp.NewMsgID()

	payload, err := usp.EncodeGet(msgID, []string{probeParamPath}, 1)
	if err != nil {
		return fmt.Errorf("probe: encode Get: %w", err)
	}
	record, err := usp.EncodeRecord(p.controllerID, c.Endpoint(), payload)
	if err != nil {
		return fmt.Errorf("probe: encode record: %w", err)
	}

	p.mu.Lock()
	p.pending[msgID] = pendingProbe{endpoint: c.Endpoint(), kind: c.Kind()}
	p.mu.Unlock()

	if err := c.Send(ctx, record); err != nil {
		p.mu.Lock()
		delete(p.pending, msgID)
		p.mu.Unlock()
		return fmt.Errorf("probe: send Get: %w", err)
	}
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

	if uspErr := usp.ErrorFromMsg(msg); uspErr != nil {
		p.log.Warn("usp probe: agent returned an error", "endpoint", from, "mtp", entry.kind, "msg_id", msgID, "error", uspErr)
		return true
	}

	getResp := msg.GetBody().GetResponse().GetGetResp()
	if getResp == nil {
		p.log.Warn("usp probe: response to probe Get was not a GetResp", "endpoint", from, "mtp", entry.kind, "msg_id", msgID, "msg_type", msg.GetHeader().GetMsgType())
		return true
	}

	for _, reqResult := range getResp.GetReqPathResults() {
		for _, resolved := range reqResult.GetResolvedPathResults() {
			for name, value := range resolved.GetResultParams() {
				p.log.Info("usp probe: parameter", "endpoint", from, "mtp", entry.kind,
					"param_path", resolved.GetResolvedPath()+name, "value", value)
			}
		}
	}
	return true
}

// forget drops every pending probe addressed to endpoint. Called from
// Handler.OnDisconnect: a probe that will never be answered must not sit
// in the map forever.
func (p *probe) forget(endpoint usp.EndpointID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, entry := range p.pending {
		if entry.endpoint == endpoint {
			delete(p.pending, id)
		}
	}
}
