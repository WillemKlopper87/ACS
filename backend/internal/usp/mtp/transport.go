// Package mtp defines the transport abstraction shared by every USP
// Message Transfer Protocol: a live connection to an agent, the
// listener that accepts such connections, and a registry that tracks
// the one live connection per endpoint id across whichever transports
// are running.
//
// It exists so the protocol core and cmd/uspc never depend on a
// specific MTP. WebSocket (Task 3) and MQTT (Task 4) each implement
// Transport and Conn; cmd/uspc (Task 5) talks only to these
// interfaces, so adding a third MTP later touches this package's
// consumers not at all.
package mtp

import (
	"context"
	"time"

	"acs/internal/usp"
)

// Kind identifies which MTP a Conn or Transport speaks. Its values are
// the exact strings the reference agent (obuspa) writes into
// Device.LocalAgent.MTP.{i}.Protocol, so a value read from the data
// model can be compared against a Kind without translation.
type Kind string

const (
	// KindWebSocket is the WebSocket MTP.
	KindWebSocket Kind = "WebSocket"
	// KindMQTT is the MQTT MTP.
	KindMQTT Kind = "MQTT"
)

// Conn is one live connection to a USP agent, however the underlying
// transport carries bytes. It is the unit the Registry tracks: at most
// one Conn per endpoint id, regardless of which Kind established it.
type Conn interface {
	// Endpoint is the id of the agent at the other end. It is fixed for
	// the life of the connection -- an agent that wants a different id
	// reconnects rather than renaming an existing Conn.
	Endpoint() usp.EndpointID
	// Kind reports which MTP this connection was established over, so a
	// caller need not type-assert to tell WebSocket and MQTT conns apart.
	Kind() Kind
	// Send writes one USP Record to the agent. It may block until the
	// underlying transport accepts the write; ctx bounds that wait.
	Send(ctx context.Context, record []byte) error
	// Close ends the connection. reason is carried into any Handler
	// callback and logging so an operator can tell a graceful shutdown
	// from a protocol error without reading a stack trace.
	Close(reason string) error
	// RemoteAddr identifies the other end for logging and diagnostics.
	// Its format is transport-specific and not meant to be parsed.
	RemoteAddr() string
}

// Inbound is one USP Record received on a Conn, handed to a Handler as
// a single value so OnRecord need not juggle three separate arguments.
type Inbound struct {
	// Conn is the connection the record arrived on.
	Conn Conn
	// Record is the raw, still-encoded USP Record protobuf.
	Record []byte
	// ReceivedAt is when the transport finished receiving the record,
	// captured here because by the time a handler gets to look at it the
	// answer may otherwise depend on how busy the handler goroutine was.
	ReceivedAt time.Time
}

// Handler reacts to the lifecycle events a Transport produces. A
// Transport implementation calls these methods; it never inspects USP
// message content itself, keeping protocol interpretation entirely on
// the Handler side of this interface.
type Handler interface {
	// OnConnect fires once a Conn is usable, before any record has been
	// read from it -- the point at which a Handler should register it.
	OnConnect(Conn)
	// OnRecord fires for every record a Conn receives.
	OnRecord(Inbound)
	// OnDisconnect fires once a Conn is no longer usable. err is nil for
	// a clean, requested close and non-nil for a transport-level failure,
	// so a Handler can distinguish the two without inspecting reason
	// strings.
	OnDisconnect(Conn, error)
}

// Transport is one MTP listener: it accepts or establishes connections
// and reports their lifecycle to a Handler. cmd/uspc runs one Transport
// per configured MTP, all feeding the same Handler and Registry, so an
// agent may reach the controller over WebSocket or MQTT interchangeably.
type Transport interface {
	// Kind identifies which MTP this Transport implements.
	Kind() Kind
	// Start begins accepting connections and runs until ctx is canceled
	// or Stop is called. h receives every connection lifecycle event.
	Start(ctx context.Context, h Handler) error
	// Stop ends the transport, closing any connections it owns. ctx
	// bounds how long an orderly shutdown may take.
	Stop(ctx context.Context) error
	// Addr reports where the transport is listening, for logging --
	// format is transport-specific.
	Addr() string
}
