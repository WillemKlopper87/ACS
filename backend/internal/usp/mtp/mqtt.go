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
	"acs/internal/usp/principal"

	mqttserver "github.com/mochi-mqtt/server/v2"
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
	// addresses inside one of these networks -- empty is permissive.
	AllowedCIDRs []*net.IPNet
	// PrincipalAuthenticator, when non-nil, turns certificate identity
	// binding and per-principal topic ACLs on. The TLS listener must have
	// already verified the presented client certificate. Lab deployments
	// leave this nil to retain explicit compatibility behavior; production
	// wiring supplies the durable principal repository.
	PrincipalAuthenticator principal.CertificateAuthenticator
}

// MQTT is a Transport that serves the USP MQTT MTP binding on an
// embedded mochi-mqtt broker.
type MQTT struct {
	cfg MQTTConfig
	log *slog.Logger

	server   *mqttserver.Server
	listener *listeners.TCP
	authHook *allowlistHook

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
	// when the broker reports the client disconnected.
	conns map[string]*mqttConn
}

var (
	_ Transport = (*MQTT)(nil)
	_ Conn      = (*mqttConn)(nil)
)

// NewMQTT validates cfg, constructs the embedded broker with an inline
// client enabled, wires the one security hook that owns CONNECT admission
// and topic authorization, and binds its TCP listener. It does not start
// accepting connections or subscribe to anything -- call Start for that.
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

	m := &MQTT{
		cfg:    cfg,
		log:    log,
		server: server,
		conns:  make(map[string]*mqttConn),
	}

	if err := server.AddHook(&mqttDisconnectHook{m: m}, nil); err != nil {
		return nil, fmt.Errorf("mtp: MQTT add disconnect hook: %w", err)
	}

	hook := &allowlistHook{
		cidrs:           cfg.AllowedCIDRs,
		log:             log,
		auth:            cfg.PrincipalAuthenticator,
		controllerTopic: cfg.ControllerTopic,
		principals:      make(map[*mqttserver.Client]*principal.Principal),
	}
	m.authHook = hook
	if err := server.AddHook(hook, nil); err != nil {
		return nil, fmt.Errorf("mtp: MQTT add security hook: %w", err)
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
	// itself. MQTT 5 publishes to the bare controller topic while MQTT
	// 3.1.1 carries its reply-to as a suffix.
	if err := m.server.Subscribe(m.cfg.ControllerTopic, mqttSubscriptionID, m.onPublish); err != nil {
		return fmt.Errorf("mtp: MQTT subscribe: %w", err)
	}
	if err := m.server.Subscribe(m.cfg.ControllerTopic+"/#", mqttWildcardSubscriptionID, m.onPublish); err != nil {
		return fmt.Errorf("mtp: MQTT subscribe wildcard: %w", err)
	}

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
// connection it owns. It is safe to call more than once.
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
// returns since the listener is bound (though not yet accepting) there.
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
// controller topics. The handler's client argument is the broker inline
// client; pk.Origin identifies the real publisher.
func (m *MQTT) onPublish(_ *mqttserver.Client, _ packets.Subscription, pk packets.Packet) {
	m.mu.Lock()
	h := m.handler
	m.mu.Unlock()
	if h == nil {
		return
	}

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

	decoded, err := usp.DecodeRecord(pk.Payload, m.cfg.ControllerEndpointID)
	if err != nil && !errors.Is(err, usp.ErrNoPayload) {
		m.log.Warn("mtp: MQTT publish carries an undecodable record, dropping", "topic", pk.TopicName, "client", pk.Origin, "error", err)
		return
	}

	originClient, ok := m.server.Clients.Get(pk.Origin)
	if !ok {
		m.log.Warn("mtp: MQTT publish from an unknown client, dropping", "topic", pk.TopicName, "client", pk.Origin)
		return
	}

	// Production principal enforcement happens before connFor: an attacker
	// must never be able to register (and thereby replace) a live connection
	// under another agent's EndpointID, even for one record.
	p := m.authHook.principalFor(originClient)
	if m.cfg.PrincipalAuthenticator != nil && p == nil {
		m.log.Warn("mtp: MQTT publish has no authenticated principal, disconnecting", "client", pk.Origin)
		_ = m.server.DisconnectClient(originClient, packets.CodeDisconnect)
		return
	}
	if err := verifyEndpointPrincipal(p, decoded.From); err != nil {
		m.log.Warn("mtp: MQTT EndpointID impersonation rejected", "client", pk.Origin, "claimed_endpoint", decoded.From, "expected_endpoint", p.EndpointID)
		_ = m.server.DisconnectClient(originClient, packets.CodeDisconnect)
		return
	}
	if err := verifyMQTTReplyTopic(p, replyTopic); err != nil {
		m.log.Warn("mtp: MQTT cross-agent reply topic rejected", "client", pk.Origin, "claimed_reply_topic", replyTopic, "expected_reply_topic", p.MQTTTopic)
		_ = m.server.DisconnectClient(originClient, packets.CodeDisconnect)
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
// if any, and stops tracking it. Principal state is keyed by the concrete
// broker client pointer and is forgotten for every disconnect, including a
// client-id takeover where the newer connection is already in m.conns.
func (m *MQTT) handleDisconnect(cl *mqttserver.Client, err error) {
	if m.authHook != nil {
		m.authHook.forget(cl)
	}

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
// back to the logical Conn it belongs to and fires Handler.OnDisconnect.
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

// allowlistHook is the embedded broker's single CONNECT-authentication and
// ACL hook. Keeping those decisions in one hook is important because mochi-
// mqtt ORs the answers from multiple auth hooks: registering a separate
// allow-all hook would silently override a denial here.
//
// In lab mode auth is nil: CONNECT is gated only by AllowedCIDRs and topic
// access retains the historical permissive behavior. In production auth is
// non-nil: a verified client certificate must resolve to a durable principal,
// and that principal is then the sole basis for topic authorization.
type allowlistHook struct {
	mqttserver.HookBase
	cidrs           []*net.IPNet
	log             *slog.Logger
	auth            principal.CertificateAuthenticator
	controllerTopic string

	principalMu sync.RWMutex
	principals  map[*mqttserver.Client]*principal.Principal
}

func (h *allowlistHook) ID() string { return "usp-allowlist" }

func (h *allowlistHook) Provides(b byte) bool {
	return b == mqttserver.OnConnectAuthenticate || b == mqttserver.OnACLCheck
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
	if h.auth == nil {
		return true
	}

	p, err := authenticateCertificate(context.Background(), h.auth, peerCertificate(cl.Net.Conn))
	if err != nil {
		h.log.Warn("mtp: rejecting MQTT client whose certificate is not an enabled USP principal", "remote", cl.Net.Remote, "error", err)
		return false
	}
	h.remember(cl, p)
	return true
}

// OnACLCheck enforces the authenticated principal's topic namespace in
// production. The broker's own inline client is trusted because it is an
// in-process controller component, not a network peer.
func (h *allowlistHook) OnACLCheck(cl *mqttserver.Client, topic string, write bool) bool {
	if cl != nil && cl.ID == mqttserver.InlineClientId {
		return true
	}
	if h.auth == nil {
		return true
	}
	p := h.principalFor(cl)
	if p == nil {
		return false
	}
	allowed := mqttTopicAllowed(p, h.controllerTopic, topic, write)
	if !allowed {
		h.log.Warn("mtp: MQTT topic ACL rejected", "client", cl.ID, "endpoint", p.EndpointID, "topic", topic, "write", write)
	}
	return allowed
}

func (h *allowlistHook) remember(cl *mqttserver.Client, p *principal.Principal) {
	h.principalMu.Lock()
	defer h.principalMu.Unlock()
	h.principals[cl] = p
}

func (h *allowlistHook) principalFor(cl *mqttserver.Client) *principal.Principal {
	if cl == nil {
		return nil
	}
	h.principalMu.RLock()
	defer h.principalMu.RUnlock()
	return h.principals[cl]
}

func (h *allowlistHook) forget(cl *mqttserver.Client) {
	h.principalMu.Lock()
	defer h.principalMu.Unlock()
	delete(h.principals, cl)
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
	// that blocks without needing an actual stalled TCP client.
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
// PUBLISH carrying the Response Topic and Content Type properties. A
// v3.1.1 agent gets a PUBLISH to its reply topic suffixed with this
// controller's own reply-to.
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

// publish performs the actual, potentially-blocking MQTT publish for Send.
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
// broker's DISCONNECT does not carry an application-defined reason string.
func (c *mqttConn) Close(reason string) error {
	c.transport.log.Info("mtp: MQTT closing connection", "client", c.client.ID, "endpoint", c.endpoint, "reason", reason)
	return c.transport.server.DisconnectClient(c.client, packets.CodeDisconnect)
}
