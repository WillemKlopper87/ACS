package mtp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
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

// mqttSubscriptionID is the inline-client subscription id Start
// registers cfg.ControllerTopic + "/#" under. There is only ever one
// such subscription per MQTT transport, so a fixed id is sufficient.
const mqttSubscriptionID = 1

// MQTTConfig configures an MQTT Transport: an embedded mochi-mqtt
// broker rather than a connection to an external one, so serving USP
// over MQTT needs no separate broker infrastructure.
type MQTTConfig struct {
	// Addr is the address the embedded broker listens on, e.g. ":1883"
	// or "127.0.0.1:0".
	Addr string
	// ControllerTopic is the topic this controller is reachable on.
	// Start subscribes to ControllerTopic + "/#": the wildcard is
	// required so an MQTT 3.1.1 agent's ".../reply-to=<escaped topic>"
	// suffix is still received under this subscription.
	ControllerTopic string
	// TLS, when non-nil, serves MQTT over TLS. When nil, the listener
	// is plaintext, which requires AllowPlaintext.
	TLS *tls.Config
	// AllowPlaintext opts into serving without TLS. NewMQTT returns an
	// error if TLS is nil and this is false.
	AllowPlaintext bool
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
// client to cfg.ControllerTopic + "/#", then runs until ctx is
// canceled or Stop is called.
func (m *MQTT) Start(ctx context.Context, h Handler) error {
	m.mu.Lock()
	m.handler = h
	m.mu.Unlock()

	if err := m.server.Serve(); err != nil {
		return fmt.Errorf("mtp: MQTT serve: %w", err)
	}

	filter := m.cfg.ControllerTopic + "/#"
	if err := m.server.Subscribe(filter, mqttSubscriptionID, m.onPublish); err != nil {
		return fmt.Errorf("mtp: MQTT subscribe: %w", err)
	}

	go func() {
		<-ctx.Done()
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

// onPublish is the inline subscription handler for cfg.ControllerTopic
// + "/#": it fires for every PUBLISH an agent sends toward this
// controller.
func (m *MQTT) onPublish(cl *mqttserver.Client, _ packets.Subscription, pk packets.Packet) {
	m.mu.Lock()
	h := m.handler
	m.mu.Unlock()
	if h == nil {
		return
	}

	// Derive the agent's reply topic: the MQTT 5 Response Topic
	// property for a v5 client, or the "/reply-to=" topic suffix for
	// v3.1.1. A record with no reply path cannot be answered, so it is
	// dropped rather than handed to h with nowhere to send a response.
	var replyTopic string
	if cl.Properties.ProtocolVersion == 5 {
		replyTopic = pk.Properties.ResponseTopic
	} else {
		var ok bool
		replyTopic, ok = ReplyToFromV311Topic(pk.TopicName, m.cfg.ControllerTopic)
		if !ok {
			replyTopic = ""
		}
	}
	if replyTopic == "" {
		m.log.Warn("mtp: MQTT publish has no reply-to, dropping", "topic", pk.TopicName, "client", cl.ID)
		return
	}

	// The transport decodes only far enough to read From, the id
	// needed to key the Conn registry -- interpreting the message
	// payload itself is cmd/uspc's job, not this transport's.
	decoded, err := usp.DecodeRecord(pk.Payload, "")
	if err != nil && !errors.Is(err, usp.ErrNoPayload) {
		m.log.Warn("mtp: MQTT publish carries an undecodable record, dropping", "topic", pk.TopicName, "client", cl.ID, "error", err)
		return
	}
	if decoded == nil {
		m.log.Warn("mtp: MQTT publish decoded to nothing, dropping", "topic", pk.TopicName, "client", cl.ID)
		return
	}

	conn := m.connFor(cl, decoded.From, replyTopic, h)

	h.OnRecord(Inbound{
		Conn:       conn,
		Record:     pk.Payload,
		ReceivedAt: time.Now(),
	})
}

// connFor returns the logical Conn for cl, creating it and firing
// OnConnect the first time cl is seen.
func (m *MQTT) connFor(cl *mqttserver.Client, endpoint usp.EndpointID, replyTopic string, h Handler) *mqttConn {
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

	conn.setReplyState(cl.Properties.ProtocolVersion, replyTopic)

	if !exists {
		h.OnConnect(conn)
	}
	return conn
}

// handleDisconnect fires OnDisconnect for the Conn associated with cl,
// if any, and stops tracking it.
func (m *MQTT) handleDisconnect(cl *mqttserver.Client, err error) {
	m.mu.Lock()
	conn, ok := m.conns[cl.ID]
	if ok {
		delete(m.conns, cl.ID)
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
func (c *mqttConn) Send(ctx context.Context, record []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	protocolVersion, replyTopic := c.replyState()
	if replyTopic == "" {
		return errors.New("mtp: MQTT Conn has no reply topic")
	}

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
