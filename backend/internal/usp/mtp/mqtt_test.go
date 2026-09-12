package mtp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"testing"
	"time"

	"acs/internal/usp"

	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// testControllerEID is the ControllerEndpointID used across this
// file's tests -- an arbitrary but fixed USP endpoint id standing in
// for what cmd/uspc will supply in practice (self::<ACS_USP_CONTROLLER_ID>).
const testControllerEID = usp.EndpointID("self::controller-test")

func TestMQTTPlaintextRequiresOptIn(t *testing.T) {
	if _, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller", ControllerEndpointID: testControllerEID}, slog.Default()); err == nil {
		t.Error("NewMQTT with no TLS and no AllowPlaintext succeeded; plaintext must be opt-in")
	}
}

func TestMQTTRequiresControllerEndpointID(t *testing.T) {
	if _, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller", AllowPlaintext: true}, slog.Default()); err == nil {
		t.Error("NewMQTT with no ControllerEndpointID succeeded; want an error")
	}
}

func TestMQTTStartStop(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller", ControllerEndpointID: testControllerEID, AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx, newRecordingHandler()); err != nil {
		t.Fatal(err)
	}
	if m.Kind() != KindMQTT {
		t.Errorf("Kind() = %q, want MQTT", m.Kind())
	}
	// The broker must be listening: a raw TCP connect succeeds.
	c, err := net.DialTimeout("tcp", m.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("broker not listening on %s: %v", m.Addr(), err)
	}
	c.Close()
	// Stop is called directly here, before the deferred cancel() ever
	// fires -- this is what exercises Start's ctx-watcher goroutine
	// waking from Stop's internal done channel rather than blocking
	// forever on <-ctx.Done().
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestMQTTStartRejectsSecondCall(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller", ControllerEndpointID: testControllerEID, AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx, newRecordingHandler()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(context.Background()) }()

	if err := m.Start(ctx, newRecordingHandler()); err == nil {
		t.Error("second Start call succeeded; want an error")
	}
}

// newMQTTAgentClient registers a synthetic client directly with m's
// broker, standing in for a real TCP-connected agent. protocolVersion
// is 4 for MQTT 3.1.1 and 5 for MQTT 5 -- mirroring what the broker
// itself sets on a client from its CONNECT packet.
func newMQTTAgentClient(t *testing.T, m *MQTT, id string, protocolVersion byte) *mqttserver.Client {
	t.Helper()
	cl := m.server.NewClient(nil, mqttListenerID, id, false)
	cl.Properties.ProtocolVersion = protocolVersion
	cl.Net.Remote = "203.0.113.5:12345"
	// A real client gets its inflight receive quota set while
	// processing its CONNECT packet; this synthetic client skips that,
	// so without this processPublish would refuse every publish with
	// "receive maximum exceeded".
	cl.State.Inflight.ResetReceiveQuota(math.MaxInt32)
	m.server.Clients.Add(cl)
	return cl
}

// TestMQTTOnPublishV5 exercises onPublish and Send end-to-end for an
// MQTT 5 agent, using the broker's own InjectPacket rather than a real
// TCP client (a real MQTT client is deliberately not a test dependency
// per the task brief). It is the regression guard for two bugs the
// first pass had: onPublish read cl.Properties.ProtocolVersion and
// cl.ID from the handler's cl parameter, but mochi-mqtt always passes
// its own inline client there for inline subscriptions -- never the
// client that actually published. The real values live on the packet:
// pk.ProtocolVersion and pk.Origin.
func TestMQTTOnPublishV5(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller", ControllerEndpointID: testControllerEID, AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	h := newRecordingHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx, h); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(context.Background()) }()

	agentCl := newMQTTAgentClient(t, m, "agent-v5", 5)

	wire, err := usp.EncodeRecord(usp.EndpointID("agent-v5"), testControllerEID, []byte("hello from v5 agent"))
	if err != nil {
		t.Fatal(err)
	}

	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   "/usp/controller",
		Payload:     wire,
		Properties: packets.Properties{
			ResponseTopic: "/usp/agent-v5",
			ContentType:   ContentTypeUSP,
		},
	}
	if err := m.server.InjectPacket(agentCl, pk); err != nil {
		t.Fatalf("InjectPacket: %v", err)
	}

	waitFor(t, h.connected, "OnConnect")
	waitFor(t, h.received, "OnRecord")

	h.mu.Lock()
	if len(h.records) != 1 {
		h.mu.Unlock()
		t.Fatalf("len(records) = %d, want 1", len(h.records))
	}
	in := h.records[0]
	h.mu.Unlock()

	if !bytes.Equal(in.Record, wire) {
		t.Errorf("Inbound.Record = %x, want %x", in.Record, wire)
	}
	if in.Conn.Endpoint() != usp.EndpointID("agent-v5") {
		t.Errorf("Inbound.Conn.Endpoint() = %q, want %q", in.Conn.Endpoint(), "agent-v5")
	}
	if in.Conn.Kind() != KindMQTT {
		t.Errorf("Inbound.Conn.Kind() = %q, want MQTT", in.Conn.Kind())
	}

	// Round-trip Send: capture what gets published to the agent's
	// reply topic and check it carries the v5 Response Topic / Content
	// Type properties, delivered via InjectPacket.
	captured := make(chan packets.Packet, 1)
	if err := m.server.Subscribe("/usp/agent-v5", 99, func(_ *mqttserver.Client, _ packets.Subscription, pk packets.Packet) {
		captured <- pk
	}); err != nil {
		t.Fatal(err)
	}

	reply := []byte("reply from controller")
	if err := in.Conn.Send(context.Background(), reply); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case pk := <-captured:
		if !bytes.Equal(pk.Payload, reply) {
			t.Errorf("published payload = %x, want %x", pk.Payload, reply)
		}
		if pk.Properties.ResponseTopic != "/usp/controller" {
			t.Errorf("published ResponseTopic = %q, want /usp/controller", pk.Properties.ResponseTopic)
		}
		if pk.Properties.ContentType != ContentTypeUSP {
			t.Errorf("published ContentType = %q, want %q", pk.Properties.ContentType, ContentTypeUSP)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Send's publish to be captured")
	}
}

// TestMQTTOnPublishV311 is TestMQTTOnPublishV5's counterpart for an
// MQTT 3.1.1 agent: the reply-to topic travels as a topic suffix
// rather than an MQTT 5 property, both inbound (parsed by
// ReplyToFromV311Topic) and outbound (built by EscapeReplyTo).
func TestMQTTOnPublishV311(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller", ControllerEndpointID: testControllerEID, AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	h := newRecordingHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx, h); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(context.Background()) }()

	agentCl := newMQTTAgentClient(t, m, "agent-v311", 4)

	wire, err := usp.EncodeRecord(usp.EndpointID("agent-v311"), testControllerEID, []byte("hello from v3.1.1 agent"))
	if err != nil {
		t.Fatal(err)
	}

	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   "/usp/controller/reply-to=" + EscapeReplyTo("/usp/agent-v311"),
		Payload:     wire,
	}
	if err := m.server.InjectPacket(agentCl, pk); err != nil {
		t.Fatalf("InjectPacket: %v", err)
	}

	waitFor(t, h.connected, "OnConnect")
	waitFor(t, h.received, "OnRecord")

	h.mu.Lock()
	if len(h.records) != 1 {
		h.mu.Unlock()
		t.Fatalf("len(records) = %d, want 1", len(h.records))
	}
	in := h.records[0]
	h.mu.Unlock()

	if !bytes.Equal(in.Record, wire) {
		t.Errorf("Inbound.Record = %x, want %x", in.Record, wire)
	}
	if in.Conn.Endpoint() != usp.EndpointID("agent-v311") {
		t.Errorf("Inbound.Conn.Endpoint() = %q, want %q", in.Conn.Endpoint(), "agent-v311")
	}

	wantReplyTopic := "/usp/agent-v311/reply-to=" + EscapeReplyTo("/usp/controller")
	captured := make(chan packets.Packet, 1)
	if err := m.server.Subscribe(wantReplyTopic, 98, func(_ *mqttserver.Client, _ packets.Subscription, pk packets.Packet) {
		captured <- pk
	}); err != nil {
		t.Fatal(err)
	}

	reply := []byte("reply from controller v3.1.1")
	if err := in.Conn.Send(context.Background(), reply); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case pk := <-captured:
		if !bytes.Equal(pk.Payload, reply) {
			t.Errorf("published payload = %x, want %x", pk.Payload, reply)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Send's publish to be captured")
	}
}

// TestMQTTSendHonoursContextTimeout proves that mqttConn.Send does not
// block past ctx's deadline even when the underlying publish call
// itself hangs -- the scenario a wedged agent socket produces in
// production, since mochi-mqtt runs inline subscription handlers
// synchronously on the publishing goroutine. The underlying publish is
// faked via publishFn (a test-only seam) rather than a real stalled
// TCP client, since that is the cheapest reliable way to force a hang
// without flaking.
func TestMQTTSendHonoursContextTimeout(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller", ControllerEndpointID: testControllerEID, AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	h := newRecordingHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx, h); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(context.Background()) }()

	agentCl := newMQTTAgentClient(t, m, "agent-wedged", 5)
	wire, err := usp.EncodeRecord(usp.EndpointID("agent-wedged"), testControllerEID, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   "/usp/controller",
		Payload:     wire,
		Properties: packets.Properties{
			ResponseTopic: "/usp/agent-wedged",
			ContentType:   ContentTypeUSP,
		},
	}
	if err := m.server.InjectPacket(agentCl, pk); err != nil {
		t.Fatalf("InjectPacket: %v", err)
	}
	waitFor(t, h.connected, "OnConnect")

	h.mu.Lock()
	conn := h.connects[0].(*mqttConn)
	h.mu.Unlock()

	// hangReleased is only closed for cleanup bookkeeping -- Send must
	// return long before the fake publish call ever unblocks.
	hangReleased := make(chan struct{})
	conn.publishFn = func(byte, string, []byte) error {
		<-hangReleased
		return nil
	}
	defer close(hangReleased)

	sendCtx, sendCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer sendCancel()

	start := time.Now()
	err = conn.Send(sendCtx, []byte("reply"))
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Send error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > time.Second {
		t.Errorf("Send took %v to return after its context expired; want it to return promptly", elapsed)
	}
}

func TestAllowlistHookRejectsDisallowedRemote(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{
		Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller",
		ControllerEndpointID: testControllerEID, AllowPlaintext: true,
		AllowedCIDRs: []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	hook := &allowlistHook{cidrs: []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")}, log: slog.Default()}
	cl := newMQTTAgentClient(t, m, "outside-allowlist", 5)
	cl.Net.Remote = "203.0.113.5:12345" // outside 10.0.0.0/8
	if hook.OnConnectAuthenticate(cl, packets.Packet{}) {
		t.Error("OnConnectAuthenticate for a disallowed remote = true, want false")
	}
}

func TestAllowlistHookAllowsPermittedRemote(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{
		Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller",
		ControllerEndpointID: testControllerEID, AllowPlaintext: true,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	hook := &allowlistHook{cidrs: []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")}, log: slog.Default()}
	cl := newMQTTAgentClient(t, m, "inside-allowlist", 5)
	cl.Net.Remote = "10.1.2.3:12345"
	if !hook.OnConnectAuthenticate(cl, packets.Packet{}) {
		t.Error("OnConnectAuthenticate for an allowed remote = false, want true")
	}
}

func TestAllowlistHookPermissiveWhenEmpty(t *testing.T) {
	hook := &allowlistHook{log: slog.Default()} // no cidrs
	m, err := NewMQTT(MQTTConfig{Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller", ControllerEndpointID: testControllerEID, AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	cl := newMQTTAgentClient(t, m, "any-remote", 5)
	cl.Net.Remote = "203.0.113.5:12345"
	if !hook.OnConnectAuthenticate(cl, packets.Packet{}) {
		t.Error("OnConnectAuthenticate with an empty allowlist = false, want true (permissive)")
	}
}

// buildMQTTConnectPacket hand-encodes a minimal, valid MQTT 3.1.1 CONNECT
// packet (clean session, no username/password/will) for clientID. This
// codebase deliberately carries no real MQTT client library dependency
// (see TestMQTTOnPublishV5's doc comment), so the two wire-level tests
// below build and parse raw MQTT bytes directly rather than pulling one
// in just for this.
//
// Fixed header: type/flags byte, then a one-byte remaining length (valid
// as long as it stays under 128, true for every clientID this file uses).
// Variable header: protocol name "MQTT", protocol level 4, connect flags
// 0x02 (clean session), a 60s keepalive. Payload: the client identifier,
// length-prefixed.
func buildMQTTConnectPacket(clientID string) []byte {
	var varHeader bytes.Buffer
	varHeader.WriteByte(0x00)
	varHeader.WriteByte(0x04)
	varHeader.WriteString("MQTT")
	varHeader.WriteByte(0x04) // protocol level: MQTT 3.1.1
	varHeader.WriteByte(0x02) // connect flags: clean session only
	varHeader.WriteByte(0x00)
	varHeader.WriteByte(0x3C) // keep alive: 60s

	var payload bytes.Buffer
	payload.WriteByte(byte(len(clientID) >> 8))
	payload.WriteByte(byte(len(clientID)))
	payload.WriteString(clientID)

	remLen := varHeader.Len() + payload.Len()

	var pkt bytes.Buffer
	pkt.WriteByte(0x10) // CONNECT
	pkt.WriteByte(byte(remLen))
	pkt.Write(varHeader.Bytes())
	pkt.Write(payload.Bytes())
	return pkt.Bytes()
}

// readMQTTConnackCode reads a CONNACK packet off conn and returns its
// return/reason code byte (0 = accepted; non-zero = refused). It fails
// the test outright on any read error or if the packet type read back
// is not CONNACK, since either would mean the test itself is broken
// rather than exercising the real refusal/acceptance path.
func readMQTTConnackCode(t *testing.T, conn net.Conn) byte {
	t.Helper()
	buf := make([]byte, 4) // CONNACK is always type/flags, remaining length 2, session-present, return code
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read CONNACK: %v", err)
	}
	if buf[0] != 0x20 {
		t.Fatalf("first response packet type = %#x, want CONNACK (0x20)", buf[0])
	}
	return buf[3]
}

// TestMQTTWireConnectRejectsDisallowedRemote is the real wire-level
// regression test for the bug fixed in allowlistHook's doc comment: it
// dials m's TCP listener directly and writes a real raw MQTT CONNECT
// packet, so it goes through the broker's actual registered hook chain
// exactly like a real agent would -- unlike
// TestAllowlistHookRejectsDisallowedRemote above, which calls
// hook.OnConnectAuthenticate directly and so could never have caught
// auth.AllowHook silently overriding it via mochi-mqtt's OR-aggregation
// (that bug shipped with 100% green tests of the direct-call kind). The
// dialing address is loopback, deliberately excluded by AllowedCIDRs, so
// a correct broker must refuse the CONNECT with a non-zero CONNACK
// return code.
func TestMQTTWireConnectRejectsDisallowedRemote(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{
		Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller",
		ControllerEndpointID: testControllerEID, AllowPlaintext: true,
		AllowedCIDRs: []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")}, // excludes 127.0.0.1
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx, newRecordingHandler()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(context.Background()) }()

	conn, err := net.DialTimeout("tcp", m.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", m.Addr(), err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write(buildMQTTConnectPacket("wire-test-disallowed")); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}

	code := readMQTTConnackCode(t, conn)
	if code == 0x00 {
		t.Error("CONNACK return code = 0x00 (accepted) for a disallowed remote, want a non-zero refusal code")
	}
}

// TestMQTTWireConnectAllowsPermittedRemote is
// TestMQTTWireConnectRejectsDisallowedRemote's positive counterpart: the
// dialing loopback address is inside AllowedCIDRs, so a correct broker
// must accept the CONNECT with a successful (0x00) CONNACK return code.
func TestMQTTWireConnectAllowsPermittedRemote(t *testing.T) {
	m, err := NewMQTT(MQTTConfig{
		Addr: "127.0.0.1:0", ControllerTopic: "/usp/controller",
		ControllerEndpointID: testControllerEID, AllowPlaintext: true,
		AllowedCIDRs: []*net.IPNet{mustParseCIDR(t, "127.0.0.0/8")}, // includes 127.0.0.1
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx, newRecordingHandler()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(context.Background()) }()

	conn, err := net.DialTimeout("tcp", m.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", m.Addr(), err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write(buildMQTTConnectPacket("wire-test-allowed")); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}

	code := readMQTTConnackCode(t, conn)
	if code != 0x00 {
		t.Errorf("CONNACK return code = %#x for a permitted remote, want 0x00 (accepted)", code)
	}
}
