package mtp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"acs/internal/usp"

	"github.com/coder/websocket"
)

// subprotocol is the mandatory USP WebSocket subprotocol (R-WS.12a). A
// server must not establish a session without it.
const subprotocol = "v1.usp"

// defaultMaxRecordBytes matches the reference agent's (obuspa) frame
// cap, used when WebSocketConfig.MaxRecordBytes is zero.
const defaultMaxRecordBytes = 5 * 1024 * 1024

// WebSocketConfig configures a WebSocket Transport.
type WebSocketConfig struct {
	// Addr is the address to listen on, e.g. ":8080" or "127.0.0.1:0".
	Addr string
	// Path is the HTTP path the USP endpoint is served on. Defaults to
	// "/usp".
	Path string
	// TLS, when non-nil, serves WebSocket over TLS. When nil, the
	// listener is plaintext, which requires AllowPlaintext.
	TLS *tls.Config
	// AllowPlaintext opts into serving without TLS. NewWebSocket
	// returns an error if TLS is nil and this is false.
	AllowPlaintext bool
	// PingInterval is currently unused by this transport: the
	// coder/websocket library answers pings automatically while the
	// read loop is active (R-WS.13), so no application-level ping
	// timer is needed. Reserved for future keepalive tuning.
	PingInterval time.Duration
	// MaxRecordBytes caps the size of a single incoming message.
	// Defaults to 5 MB, matching the reference agent's frame cap.
	MaxRecordBytes int64
}

// WebSocket is a Transport that serves the USP WebSocket MTP binding.
type WebSocket struct {
	cfg WebSocketConfig
	log *slog.Logger

	listener net.Listener
	server   *http.Server

	mu   sync.Mutex
	addr string
}

// NewWebSocket validates cfg and constructs a WebSocket transport. It
// does not start listening -- call Start for that.
func NewWebSocket(cfg WebSocketConfig, log *slog.Logger) (*WebSocket, error) {
	if cfg.TLS == nil && !cfg.AllowPlaintext {
		return nil, errors.New("mtp: WebSocket requires TLS unless AllowPlaintext is set")
	}
	if cfg.Path == "" {
		cfg.Path = "/usp"
	}
	if cfg.MaxRecordBytes == 0 {
		cfg.MaxRecordBytes = defaultMaxRecordBytes
	}
	if log == nil {
		log = slog.Default()
	}
	return &WebSocket{cfg: cfg, log: log}, nil
}

// Kind identifies this Transport as the WebSocket MTP.
func (w *WebSocket) Kind() Kind { return KindWebSocket }

// Start begins accepting WebSocket connections and runs until ctx is
// canceled or Stop is called.
func (w *WebSocket) Start(ctx context.Context, h Handler) error {
	ln, err := net.Listen("tcp", w.cfg.Addr)
	if err != nil {
		return fmt.Errorf("mtp: WebSocket listen: %w", err)
	}
	if w.cfg.TLS != nil {
		ln = tls.NewListener(ln, w.cfg.TLS)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(w.cfg.Path, w.handle(ctx, h))

	w.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	w.mu.Lock()
	w.listener = ln
	w.addr = ln.Addr().String()
	w.mu.Unlock()

	go func() {
		if err := w.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			w.log.Error("mtp: WebSocket server exited", "error", err)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = w.Stop(context.Background())
	}()

	return nil
}

// Stop shuts the HTTP server down, closing the listener and any
// connections it owns.
func (w *WebSocket) Stop(ctx context.Context) error {
	w.mu.Lock()
	server := w.server
	w.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

// Addr reports the bound listen address, valid after Start returns.
func (w *WebSocket) Addr() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.addr
}

func (w *WebSocket) handle(ctx context.Context, h Handler) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		// R-WS.12a: a server must not establish a session without the
		// agent having offered the mandatory subprotocol. websocket.Accept
		// would otherwise negotiate an empty subprotocol and succeed, so
		// this is checked before Accept rather than only after.
		if !offersSubprotocol(r.Header.Get("Sec-WebSocket-Protocol"), subprotocol) {
			http.Error(rw, "missing required Sec-WebSocket-Protocol: "+subprotocol, http.StatusBadRequest)
			return
		}

		// R-WS.10b/10c: the agent's endpoint id travels in the eid query
		// parameter, percent-encoded.
		rawEID := r.URL.Query().Get("eid")
		if rawEID == "" {
			http.Error(rw, "missing eid query parameter", http.StatusBadRequest)
			return
		}
		decoded, err := usp.PercentDecodeUSP(rawEID)
		if err != nil {
			http.Error(rw, "invalid eid: "+err.Error(), http.StatusBadRequest)
			return
		}

		conn, err := websocket.Accept(rw, r, &websocket.AcceptOptions{
			Subprotocols: []string{subprotocol},
		})
		if err != nil {
			w.log.Warn("mtp: WebSocket accept failed", "error", err)
			return
		}

		// Defence in depth: Accept only negotiates the subprotocol if the
		// client offered it, but confirm the result explicitly.
		if conn.Subprotocol() != subprotocol {
			conn.Close(websocket.StatusProtocolError, "subprotocol "+subprotocol+" required")
			return
		}

		conn.SetReadLimit(w.cfg.MaxRecordBytes)

		wc := &wsConn{
			conn:       conn,
			endpoint:   usp.EndpointID(decoded),
			remoteAddr: r.RemoteAddr,
		}

		h.OnConnect(wc)
		wc.readLoop(ctx, h)
	}
}

// offersSubprotocol reports whether header (a comma-separated list per
// RFC 6455) contains want.
func offersSubprotocol(header, want string) bool {
	for _, tok := range strings.Split(header, ",") {
		if strings.TrimSpace(tok) == want {
			return true
		}
	}
	return false
}

// wsConn implements Conn over a coder/websocket connection.
type wsConn struct {
	conn       *websocket.Conn
	endpoint   usp.EndpointID
	remoteAddr string

	disconnectOnce sync.Once
}

func (c *wsConn) Endpoint() usp.EndpointID { return c.endpoint }
func (c *wsConn) Kind() Kind               { return KindWebSocket }
func (c *wsConn) RemoteAddr() string       { return c.remoteAddr }

func (c *wsConn) Send(ctx context.Context, record []byte) error {
	return c.conn.Write(ctx, websocket.MessageBinary, record)
}

func (c *wsConn) Close(reason string) error {
	return c.conn.Close(websocket.StatusNormalClosure, reason)
}

// readLoop runs for the life of the connection: coder/websocket only
// answers control-frame pings while a Read is outstanding (R-WS.13), so
// this must stay active the whole time. It fires OnDisconnect exactly
// once, from this loop's exit path, guarded by disconnectOnce so a
// concurrent Close cannot double-fire it.
func (c *wsConn) readLoop(ctx context.Context, h Handler) {
	var terminalErr error
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				terminalErr = nil
			} else {
				terminalErr = err
			}
			break
		}
		if typ != websocket.MessageBinary {
			// R-WS.14: a frame that is not binary closes the connection.
			// Conn.Close performs the full close handshake and waits (up
			// to 5s) for the peer's close frame in reply; the peer cannot
			// send that reply until it observes this side disconnect, so
			// the handshake is started in the background and the read
			// loop exits -- and fires OnDisconnect -- immediately.
			go c.conn.Close(websocket.StatusUnsupportedData, "only binary frames are accepted")
			terminalErr = fmt.Errorf("mtp: WebSocket received non-binary frame type %v", typ)
			break
		}
		h.OnRecord(Inbound{
			Conn:       c,
			Record:     data,
			ReceivedAt: time.Now(),
		})
	}
	c.disconnectOnce.Do(func() {
		h.OnDisconnect(c, terminalErr)
	})
}
