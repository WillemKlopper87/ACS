package mtp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"acs/internal/usp"

	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// mqttListenerID is the id this transport registers its TCP listener
// under with the embedded broker.
const mqttListenerID = "usp"

// mqttSubscriptionID and mqttWildcardSubscriptionID are the
// inline-client subscription ids Start registers cfg.ControllerTopic
// and cfg.ControllerTopic + "/#" under, respectively. Fixed ids are
// sufficient since there is only ever one such pair of subscriptions
// per MQTT transport.
const (
	mqttSubscriptionID         = 1
	mqttWildcardSubscriptionID = 2
)

// MQTTConfig configures an MQTT Transport: an embedded mochi-mqtt
// broker rather than a connection to an external one, so serving USP
// over MQTT needs no separate broker infrastructure.
type MQTTConfig struct {
	// Addr is the address the embedded broker listens on, e.g. ":1883"
	// or "127.0.0.1:0".
	Addr string
	// ControllerTopic is the topic this controller is reachable on.
	// Start subscribes to both ControllerTopic itself (an MQTT 5 agent
	// publishes there directly) and ControllerTopic + "/#" (an MQTT
	// 3.1.1 agent's ".../reply-to=<escaped topic>" suffix needs the
	// wildcard).
	ControllerTopic string
	// ControllerEndpointID is this controller's own USP endpoint id --
	// the "to_id" a well-formed inbound Record must carry. It is passed
	// to usp.DecodeRecord so the transport can read the record's
	// from_id; NewMQTT requires it to be set, since without it every
	// inbound record would fail that address check and be dropped.
	ControllerEndpointID usp.EndpointID
	// TLS, when non-nil, serves MQTT over TLS. When nil, the listener
	// is plaintext, which requires AllowPlaintext.
	TLS *tls.Config
	// AllowPlaintext opts into serving without TLS. NewMQTT returns an
	// error if TLS is nil and this is false.
	AllowPlaintext bool
	// AllowedCIDRs, when non-empty, restricts accepted CONNECTs to remote
	// addresses inside one of these networks -- empty is permissive
	// (design S2.1). Enforced by allowlistHook.OnConnectAuthenticate: a
	// rejected client gets a negative CONNACK and then the connection is
	// closed, so no MQTT session is ever established.
	AllowedCIDRs []*net.IPNet
}

// MQTT is a Transport that serves the USP MQTT MTP binding on an
// embedded mochi-mqtt broker.
type MQTT struct {
	cfg MQTTConfig
	log *slog.Logger

	server   *mqttserver.Server
	listener *listeners.TCP

	stopOnce sync.Once
	stopErr  error

	mu      sync.Mutex
	started bool
	// done is closed by Stop, so Start's ctx-watcher goroutine wakes up
	// and returns even when the caller never cancels ctx -- e.g. a
	// caller that calls Stop directly, as TestMQTTStartStop does.
	done    chan struct{}
	handler Handler
	// conns tracks the one logical Conn per broker client id: a Conn
	// exists from the first record received from that client and ends
	// when the broker reports the client disconnected (see the
	// disconnect hook below).
	conns map[string]*mqttConn
}

var (
	_ Transport = (*MQTT)(nil)
	_ Conn      = (*mqttConn)(nil)
)

// NewMQTT validates cfg, constructs the embedded broker with an inline
// client enabled, wires the allow-all auth hook -- superseded by an
// allowlist hook in a later task -- and binds its TCP listener. It does
// not start accepting connections or subscribe to anything -- call
// Start for that.
func NewMQTT(cfg MQTTConfig, log *slog.Logger) (*MQTT, error) {
	if cfg.TLS == nil && !cfg.AllowPlaintext {
		return nil, errors.New("mtp: MQTT requires TLS unless AllowPlaintext is set")
	}
	if cfg.ControllerEndpointID == "" {
		return nil, errors.New("mtp: MQTT requires ControllerEndpointID")
	}
	if log == nil {
		log = slog.Default()
	}

	server := mqttserver.New(&mqttserver.Options{InlineClient: true})
	if err := server.AddHook(new(auth.AllowHook), nil); err != nil {
		return nil, fmt.Errorf("mtp: MQTT add auth hook: %w", err)
	}

	m := &MQTT{
		cfg:    cfg,
		log:    log,
		server: server,
		conns:  make(map[string]*mqttConn),
	}

	if err := server.AddHook(&mqttDisconnectHook{m: m}, nil); err != nil {
		return nil, fmt.Errorf("mtp: MQTT add disconnect hook: %w", err)
	}

	if len(cfg.AllowedCIDRs) > 0 {
		if err := server.AddHook(&allowlistHook{cidrs: cfg.AllowedCIDRs, log: log}, nil); err != nil {
			return nil, fmt.Errorf("mtp: MQTT add allowlist hook: %w", err)
		}
	}

	ln := listeners.NewTCP(listeners.Config{ID: mqttListenerID, Address: cfg.Addr, TLSConfig: cfg.TLS})
	if err := server.AddListener(ln); err != nil {
		return nil, fmt.Errorf("mtp: MQTT add listener: %w", err)
	}
	m.listener = ln

	return m, nil
}

// Kind identifies this Transport as the MQTT MTP.
func (m *MQTT) Kind() Kind { return KindMQTT }

// Start begins serving the embedded broker and subscribes the inline
// client to cfg.ControllerTopic and cfg.ControllerTopic + "/#", then
// runs until ctx is canceled or Stop is called. Calling Start more
// than once returns an error rather than re-Serve-ing and
// double-subscribing.
func (m *MQTT) Start(ctx context.Context, h Handler) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return errors.New("mtp: MQTT already started")
	}
	m.started = true
	m.handler = h
	done := make(chan struct{})
	m.done = done
	m.mu.Unlock()

	if err := m.server.Serve(); err != nil {
		return fmt.Errorf("mtp: MQTT serve: %w", err)
	}

	// Two subscriptions, not one: mochi-mqtt's inline-subscription
	// matching does not treat "<topic>/#" as covering the bare <topic>
	// itself (confirmed against v2.7.9 -- a publish to exactly
	// cfg.ControllerTopic with no further path segment is not
	// delivered to a "<topic>/#" inline subscription, only a genuinely
	// nested one is). An MQTT 5 agent publishes straight to
	// cfg.ControllerTopic (it has no need for a reply-to topic suffix,
	// carrying the Response Topic property instead), so without the
	// exact-topic subscription every v5 agent's record would silently
	// never reach onPublish.
	if err := m.server.Subscribe(m.cfg.ControllerTopic, mqttSubscriptionID, m.onPublish); err != nil {
		return fmt.Errorf("mtp: MQTT subscribe: %w", err)
	}
	if err := m.server.Subscribe(m.cfg.ControllerTopic+"/#", mqttWildcardSubscriptionID, m.onPublish); err != nil {
		return fmt.Errorf("mtp: MQTT subscribe wildcard: %w", err)
	}

	// done is closed by Stop so this goroutine still returns when Stop
	// is called directly, without ctx ever being canceled -- otherwise
	// it would block on <-ctx.Done() forever and leak.
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		_ = m.Stop(context.Background())
	}()

	return nil
}

// Stop closes the embedded broker, which in turn closes every
// connection it owns. It is safe to call more than once -- Start's own
// ctx-cancellation goroutine also calls Stop -- only the first call
// does the work.
func (m *MQTT) Stop(_ context.Context) error {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		done := m.done
		m.mu.Unlock()
		if done != nil {
			close(done)
		}
		m.stopErr = m.server.Close()
	})
	return m.stopErr
}

// Addr reports the listener's bound address, valid after NewMQTT
// returns since the listener is bound (though not yet accepting)
// there.
func (m *MQTT) Addr() string {
	return m.listener.Address()
}

// inlineClient returns the broker's built-in inline client, used to
// inject outbound publishes that carry MQTT 5 properties.
func (m *MQTT) inlineClient() (*mqttserver.Client, error) {
	cl, ok := m.server.Clients.Get(mqttserver.InlineClientId)
	if !ok {
		return nil, errors.New("mtp: MQTT inline client not registered")
	}
	return cl, nil
}

// onPublish is the inline subscription handler registered for both
// cfg.ControllerTopic and cfg.ControllerTopic + "/#" (see the two
// Subscribe calls in Start): it fires for every PUBLISH an agent sends
// toward this controller, whether addressed to the bare controller
// topic (MQTT 5) or a nested one (MQTT 3.1.1's reply-to suffix).
//
// The cl the broker passes to an inline subscription handler is always
// the broker's own inline client, never the client that actually
// published -- mochi-mqtt's publishToSubscribers calls every inline
// handler as `handler(s.inlineClient, sub, pk)` regardless of who
// published. The real publisher's identity and protocol version travel
// on the packet itself instead: pk.Origin (set from the publishing
// client's id in processPublish) and pk.ProtocolVersion (inherited
// from the publishing client when the packet was first read). Using cl
// here instead of pk.Origin/pk.ProtocolVersion would key every Conn
// under the inline client's id and always see protocol version 4.
func (m *MQTT) onPublish(_ *mqttserver.Client, _ packets.Subscription, pk packets.Packet) {
	m.mu.Lock()
	h := m.handler
	m.mu.Unlock()
	if h == nil {
		return
	}

	// Derive the agent's reply topic: the MQTT 5 Response Topic
	// property for a v5 publish, or the "/reply-to=" topic suffix for
	// v3.1.1. A record with no reply path cannot be answered, so it is
	// dropped rather than handed to h with nowhere to send a response.
	var replyTopic string
	if pk.ProtocolVersion == 5 {
		replyTopic = pk.Properties.ResponseTopic
	} else {
		var ok bool
		replyTopic, ok = ReplyToFromV311Topic(pk.TopicName, m.cfg.ControllerTopic)
		if !ok {
			replyTopic = ""
		}
	}
	if replyTopic == "" {
		m.log.Warn("mtp: MQTT publish has no reply-to, dropping", "topic", pk.TopicName, "client", pk.Origin)
		return
	}

	// The transport decodes only far enough to read From, the id
	// needed to key the Conn registry -- interpreting the message
	// payload itself is cmd/uspc's job, not this transport's.
	decoded, err := usp.DecodeRecord(pk.Payload, m.cfg.ControllerEndpointID)
	if err != nil && !errors.Is(err, usp.ErrNoPayload) {
		m.log.Warn("mtp: MQTT publish carries an undecodable record, dropping", "topic", pk.TopicName, "client", pk.Origin, "error", err)
		return
	}
	// Every path above either returned or guarantees decoded != nil:
	// DecodeRecord always populates it before returning when the error
	// is nil or ErrNoPayload, and any other error already returned above.

	originClient, ok := m.server.Clients.Get(pk.Origin)
	if !ok {
		m.log.Warn("mtp: MQTT publish from an unknown client, dropping", "topic", pk.TopicName, "client", pk.Origin)
		return
	}

	conn := m.connFor(originClient, decoded.From, pk.ProtocolVersion, replyTopic, h)

	h.OnRecord(Inbound{
		Conn:       conn,
		Record:     pk.Payload,
		ReceivedAt: time.Now(),
	})
}

// connFor returns the logical Conn for cl, creating it and firing
// OnConnect the first time cl is seen.
func (m *MQTT) connFor(cl *mqttserver.Client, endpoint usp.EndpointID, protocolVersion byte, replyTopic string, h Handler) *mqttConn {
	m.mu.Lock()
	conn, exists := m.conns[cl.ID]
	if !exists {
		conn = &mqttConn{
			transport:  m,
			client:     cl,
			endpoint:   endpoint,
			remoteAddr: cl.Net.Remote,
		}
		m.conns[cl.ID] = conn
	}
	m.mu.Unlock()

	conn.setReplyState(protocolVersion, replyTopic)

	if !exists {
		h.OnConnect(conn)
	}
	return conn
}

// handleDisconnect fires OnDisconnect for the Conn associated with cl,
// if any, and stops tracking it.
//
// cl.ID alone is not enough to identify which Conn this disconnect
// belongs to: if an agent reconnects quickly enough, a new *Client for
// the same broker client id can already be tracked under m.conns[cl.ID]
// by the time this fires for the old, now-stale *Client. Comparing the
// tracked Conn's client pointer against cl guards that reconnect
// takeover race -- a stale disconnect must not evict (or report as
// disconnected) the connection that has already replaced it.
func (m *MQTT) handleDisconnect(cl *mqttserver.Client, err error) {
	m.mu.Lock()
	conn, ok := m.conns[cl.ID]
	if ok && conn.client == cl {
		delete(m.conns, cl.ID)
	} else {
		ok = false
	}
	h := m.handler
	m.mu.Unlock()

	if !ok || h == nil {
		return
	}
	h.OnDisconnect(conn, err)
}

// mqttDisconnectHook maps the broker's client-disconnect notification
// back to the logical Conn it belongs to and fires Handler.OnDisconnect
// for it.
type mqttDisconnectHook struct {
	mqttserver.HookBase
	m *MQTT
}

func (h *mqttDisconnectHook) ID() string { return "usp-mtp-disconnect" }

func (h *mqttDisconnectHook) Provides(b byte) bool {
	return b == mqttserver.OnDisconnect
}

func (h *mqttDisconnectHook) OnDisconnect(cl *mqttserver.Client, err error, _ bool) {
	h.m.handleDisconnect(cl, err)
}

// allowlistHook rejects a CONNECT from a remote address outside cidrs,
// the network-level half of the USP agent allowlist (design S2.1). It is
// registered alongside auth.AllowHook, not instead of it: AllowHook still
// grants topic pub/sub access (unrelated, out of this plan's scope, S7 of
// the design doc); this hook only gates whether a CONNECT is accepted at
// all. OnConnectAuthenticate returning false makes mochi-mqtt refuse the
// CONNECT: the broker sends a negative CONNACK (connection refused) and
// then closes the connection -- no MQTT session is ever established, so no
// USP record is ever exchanged with a rejected client.
type allowlistHook struct {
	mqttserver.HookBase
	cidrs []*net.IPNet
	log   *slog.Logger
}

func (h *allowlistHook) ID() string { return "usp-allowlist" }

func (h *allowlistHook) Provides(b byte) bool {
	return b == mqttserver.OnConnectAuthenticate
}

func (h *allowlistHook) OnConnectAuthenticate(cl *mqttserver.Client, _ packets.Packet) bool {
	host, _, err := net.SplitHostPort(cl.Net.Remote)
	if err != nil {
		h.log.Warn("mtp: rejecting MQTT client with unparseable remote address", "remote", cl.Net.Remote, "error", err)
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ipAllowed(h.cidrs, ip) {
		h.log.Warn("mtp: rejecting MQTT client from a disallowed network", "remote", cl.Net.Remote)
		return false
	}
	return true
}

// mqttConn implements Conn over one logical MQTT session: the client
// connection identified by the broker's client id, from the first
// record it sends until the broker reports it disconnected.
type mqttConn struct {
	transport  *MQTT
	client     *mqttserver.Client
	endpoint   usp.EndpointID
	remoteAddr string

	mu              sync.RWMutex
	protocolVersion byte
	replyTopic      string

	// publishFn is what Send races against ctx; it defaults to c.publish
	// and is only ever overridden in tests, to simulate a publish call
	// that blocks (standing in for a real wedged agent socket) without
	// needing an actual stalled TCP client.
	publishFn func(protocolVersion byte, replyTopic string, record []byte) error
}

func (c *mqttConn) setReplyState(protocolVersion byte, replyTopic string) {
	c.mu.Lock()
	c.protocolVersion = protocolVersion
	c.replyTopic = replyTopic
	c.mu.Unlock()
}

func (c *mqttConn) replyState() (byte, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.protocolVersion, c.replyTopic
}

func (c *mqttConn) Endpoint() usp.EndpointID { return c.endpoint }
func (c *mqttConn) Kind() Kind               { return KindMQTT }
func (c *mqttConn) RemoteAddr() string       { return c.remoteAddr }

// Send publishes record to the agent's reply topic. A v5 agent gets a
// PUBLISH carrying the Response Topic and Content Type properties,
// delivered via InjectPacket -- the only path that carries v5
// properties on an outbound publish. A v3.1.1 agent gets a PUBLISH to
// its reply topic suffixed with this controller's own reply-to, so it
// knows where to address its next record.
//
// Both InjectPacket and Publish take no context and can block: mochi-
// mqtt v2.7.9 runs every inline subscription handler synchronously,
// on the publishing client's own broker read goroutine
// (publishToSubscribers -> inlineSubscription.Handler), so a wedged
// MQTT agent socket can block a publish to it indefinitely. Send
// therefore runs the actual publish in a goroutine and races it
// against ctx, returning ctx.Err() on timeout. The abandoned goroutine
// may still complete the publish in the background after Send has
// returned -- that is an accepted tradeoff here: a late or lost reply
// to a connection already deemed wedged is harmless.
func (c *mqttConn) Send(ctx context.Context, record []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	protocolVersion, replyTopic := c.replyState()
	if replyTopic == "" {
		return errors.New("mtp: MQTT Conn has no reply topic")
	}

	fn := c.publishFn
	if fn == nil {
		fn = c.publish
	}

	done := make(chan error, 1)
	go func() {
		done <- fn(protocolVersion, replyTopic, record)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// publish performs the actual, potentially-blocking MQTT publish for
// Send.
func (c *mqttConn) publish(protocolVersion byte, replyTopic string, record []byte) error {
	if protocolVersion == 5 {
		inline, err := c.transport.inlineClient()
		if err != nil {
			return err
		}
		pk := packets.Packet{
			FixedHeader: packets.FixedHeader{Type: packets.Publish},
			TopicName:   replyTopic,
			Payload:     record,
			Properties: packets.Properties{
				ResponseTopic: c.transport.cfg.ControllerTopic,
				ContentType:   ContentTypeUSP,
			},
		}
		return c.transport.server.InjectPacket(inline, pk)
	}

	topic := replyTopic + replyToKey + EscapeReplyTo(c.transport.cfg.ControllerTopic)
	return c.transport.server.Publish(topic, record, false, 0)
}

// Close disconnects the underlying MQTT client. reason is logged; the
// broker's DISCONNECT does not carry an application-defined reason
// string.
func (c *mqttConn) Close(reason string) error {
	c.transport.log.Info("mtp: MQTT closing connection", "client", c.client.ID, "endpoint", c.endpoint, "reason", reason)
	return c.transport.server.DisconnectClient(c.client, packets.CodeDisconnect)
}
