package mtp

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"acs/internal/usp"

	"github.com/coder/websocket"
)

type recordingHandler struct {
	mu           sync.Mutex
	connects     []Conn
	records      []Inbound
	disconnects  []Conn
	connected    chan struct{}
	received     chan struct{}
	disconnected chan struct{}
	// onDisconnect, when set, is invoked from OnDisconnect before the
	// disconnected channel is signaled -- used to exercise a Handler
	// that calls Conn.Close from its own OnDisconnect callback.
	onDisconnect func(Conn)
}

func newRecordingHandler() *recordingHandler {
	return &recordingHandler{
		connected: make(chan struct{}, 16), received: make(chan struct{}, 16), disconnected: make(chan struct{}, 16),
	}
}
func (h *recordingHandler) OnConnect(c Conn) {
	h.mu.Lock()
	h.connects = append(h.connects, c)
	h.mu.Unlock()
	h.connected <- struct{}{}
}
func (h *recordingHandler) OnRecord(in Inbound) {
	h.mu.Lock()
	h.records = append(h.records, in)
	h.mu.Unlock()
	h.received <- struct{}{}
}
func (h *recordingHandler) OnDisconnect(c Conn, _ error) {
	h.mu.Lock()
	h.disconnects = append(h.disconnects, c)
	hook := h.onDisconnect
	h.mu.Unlock()
	if hook != nil {
		hook(c)
	}
	h.disconnected <- struct{}{}
}

func waitFor(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func startWS(t *testing.T, h Handler) (*WebSocket, string) {
	t.Helper()
	ws, err := NewWebSocket(WebSocketConfig{Addr: "127.0.0.1:0", AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = ws.Stop(context.Background()) })
	if err := ws.Start(ctx, h); err != nil {
		t.Fatal(err)
	}
	return ws, "ws://" + ws.Addr() + "/usp"
}

func dial(t *testing.T, url string, subprotocols ...string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: subprotocols})
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return c
}

func TestWebSocketRequiresSubprotocol(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// No subprotocol offered: the server must not establish a session.
	c, resp, err := websocket.Dial(ctx, url+"?eid=os%3A%3A012345-AAAA", nil)
	if err == nil {
		c.CloseNow()
		t.Fatal("dial without v1.usp succeeded; R-WS.12a requires refusal")
	}
	if resp != nil && resp.StatusCode == http.StatusSwitchingProtocols {
		t.Error("server upgraded without the v1.usp subprotocol")
	}
}

func TestWebSocketEndpointFromQuery(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	// eid=os::012345-AAAA, with "::" percent-encoded per R-WS.10b/10c.
	c := dial(t, url+"?eid=os%3A%3A012345-AAAA", "v1.usp")
	defer c.CloseNow()
	waitFor(t, h.connected, "OnConnect")
	h.mu.Lock()
	got := h.connects[0].Endpoint()
	h.mu.Unlock()
	if got != usp.EndpointID("os::012345-AAAA") {
		t.Errorf("Endpoint() = %q, want os::012345-AAAA", got)
	}
	if h.connects[0].Kind() != KindWebSocket {
		t.Errorf("Kind() = %q, want WebSocket", h.connects[0].Kind())
	}
}

func TestWebSocketMissingEIDRejected(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"v1.usp"}})
	if err == nil {
		c.CloseNow()
		t.Fatal("dial without eid succeeded; the agent must identify itself in the handshake")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %v, want 400 before upgrade", resp)
	}
}

func TestWebSocketDeliversBinaryRecord(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	c := dial(t, url+"?eid=os%3A%3A012345-AAAA", "v1.usp")
	defer c.CloseNow()
	waitFor(t, h.connected, "OnConnect")
	if err := c.Write(context.Background(), websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, h.received, "OnRecord")
	h.mu.Lock()
	in := h.records[0]
	h.mu.Unlock()
	if string(in.Record) != "\x01\x02\x03" {
		t.Errorf("Record = %v, want [1 2 3]", in.Record)
	}
	if in.Conn.Endpoint() != "os::012345-AAAA" {
		t.Errorf("Inbound.Conn.Endpoint() = %q", in.Conn.Endpoint())
	}
	if in.ReceivedAt.IsZero() {
		t.Error("ReceivedAt not set")
	}
}

func TestWebSocketRejectsTextFrame(t *testing.T) {
	h := newRecordingHandler()
	// The transport closes synchronously, on the read loop, before firing
	// OnDisconnect (see websocket.go), so a Handler that calls Conn.Close
	// from OnDisconnect must not be able to overwrite the StatusUnsupportedData
	// close already sent -- exercise that race here.
	h.onDisconnect = func(c Conn) { _ = c.Close("cleanup") }
	_, url := startWS(t, h)
	c := dial(t, url+"?eid=os%3A%3A012345-AAAA", "v1.usp")
	defer c.CloseNow()
	waitFor(t, h.connected, "OnConnect")

	// Read concurrently: the server's close handshake needs an active
	// reader on this side to ack it, and that ack must not wait on
	// OnDisconnect (which itself waits on the server's close completing).
	type readResult struct {
		err error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		_, _, err := c.Read(context.Background())
		resultCh <- readResult{err: err}
	}()

	_ = c.Write(context.Background(), websocket.MessageText, []byte("not a record"))
	waitFor(t, h.disconnected, "OnDisconnect after a text frame")

	// The client must observe a close status, not a silent drop.
	select {
	case res := <-resultCh:
		if websocket.CloseStatus(res.err) != websocket.StatusUnsupportedData {
			t.Errorf("client saw close status %v, want StatusUnsupportedData (R-WS.14)", websocket.CloseStatus(res.err))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for client Read after text frame")
	}
}

func TestWebSocketSend(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	c := dial(t, url+"?eid=os%3A%3A012345-AAAA", "v1.usp")
	defer c.CloseNow()
	waitFor(t, h.connected, "OnConnect")
	h.mu.Lock()
	server := h.connects[0]
	h.mu.Unlock()
	if err := server.Send(context.Background(), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	typ, data, err := c.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary || string(data) != "hello" {
		t.Errorf("client received (%v, %q), want (Binary, hello)", typ, data)
	}
}

func TestWebSocketDisconnectOnce(t *testing.T) {
	h := newRecordingHandler()
	_, url := startWS(t, h)
	c := dial(t, url+"?eid=os%3A%3A012345-AAAA", "v1.usp")
	waitFor(t, h.connected, "OnConnect")
	_ = c.Close(websocket.StatusNormalClosure, "bye")
	waitFor(t, h.disconnected, "OnDisconnect")
	select {
	case <-h.disconnected:
		t.Error("OnDisconnect fired twice for one connection")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestWebSocketPlaintextRequiresOptIn(t *testing.T) {
	if _, err := NewWebSocket(WebSocketConfig{Addr: "127.0.0.1:0"}, slog.Default()); err == nil {
		t.Error("NewWebSocket with no TLS and no AllowPlaintext succeeded; plaintext must be opt-in")
	}
}

func TestWebSocketStopClosesConnections(t *testing.T) {
	h := newRecordingHandler()
	ws, err := NewWebSocket(WebSocketConfig{Addr: "127.0.0.1:0", AllowPlaintext: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	// Start with context.Background(): nothing external ever cancels this
	// transport's ctx, so Stop alone must be the thing that tears the
	// connection down (Transport.Stop's "closing any connections it owns").
	if err := ws.Start(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })

	c := dial(t, "ws://"+ws.Addr()+"/usp?eid=os%3A%3A012345-AAAA", "v1.usp")
	defer c.CloseNow()
	waitFor(t, h.connected, "OnConnect")

	if err := ws.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitFor(t, h.disconnected, "OnDisconnect after Stop")

	if _, _, err := c.Read(context.Background()); err == nil {
		t.Error("client Read succeeded after server Stop; want a close error")
	}
}
