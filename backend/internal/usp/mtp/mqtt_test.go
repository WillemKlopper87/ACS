package mtp

import (
	"bytes"
	"context"
	"errors"
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
