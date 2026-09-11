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

	mu       sync.Mutex
	started  bool
	listener net.Listener
	server   *http.Server
	addr     string
	// cancel ends runCtx (see Start), the context every connection's read
	// loop is bound to. Canceling it makes coder/websocket force-close
	// each outstanding Read immediately, so Stop by itself -- without
	// depending on the caller's ctx -- closes every connection this
	// transport owns, per the Transport.Stop contract.
	cancel context.CancelFunc
}

var (
	_ Transport = (*WebSocket)(nil)
	_ Conn      = (*wsConn)(nil)
)

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
// canceled or Stop is called. Calling Start more than once returns an
// error rather than leaking a second listener/server/goroutine.
func (w *WebSocket) Start(ctx context.Context, h Handler) error {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return errors.New("mtp: WebSocket already started")
	}
	w.started = true
	w.mu.Unlock()

	ln, err := net.Listen("tcp", w.cfg.Addr)
	if err != nil {
		return fmt.Errorf("mtp: WebSocket listen: %w", err)
	}
	if w.cfg.TLS != nil {
		ln = tls.NewListener(ln, w.cfg.TLS)
	}

	// runCtx bounds every connection's read loop rather than ctx directly,
	// so Stop can force all of them closed (via cancel) regardless of
	// whether the caller ever cancels ctx.
	runCtx, cancel := context.WithCancel(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc(w.cfg.Path, w.handle(runCtx, h))

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	w.mu.Lock()
	w.listener = ln
	w.addr = ln.Addr().String()
	w.server = server
	w.cancel = cancel
	w.mu.Unlock()

	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
	cancel := w.cancel
	w.mu.Unlock()

	// Cancel first: every read loop's outstanding Read is bound to
	// runCtx, and coder/websocket force-closes a connection as soon as
	// that context is canceled, unblocking each read loop without
	// waiting on a graceful close handshake with its peer.
	if cancel != nil {
		cancel()
	}
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
			if err := conn.Close(websocket.StatusProtocolError, "subprotocol "+subprotocol+" required"); err != nil {
				w.log.Warn("mtp: WebSocket close after subprotocol mismatch failed", "error", err)
			}
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

// Close ends the connection with StatusNormalClosure and reason. Per
// the controller ruling on R-WS.16, Conn.Close keeps this single-arg
// shape and always sends StatusNormalClosure with the given reason
// text; only transport-detected protocol violations use other status
// codes (non-binary frame -> StatusUnsupportedData, missing
// subprotocol -> StatusProtocolError), and those are issued directly
// by the transport, not through this method.
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
			// R-WS.14: a frame that is not binary closes the connection
			// with StatusUnsupportedData. This runs synchronously, on the
			// read loop itself, and completes before OnDisconnect fires:
			// coder/websocket's Close is otherwise idempotent-but-racy --
			// "additional calls to Close are no-ops" only once the first
			// call has actually written its close frame -- so a detached
			// goroutine here could let a Handler's own Close call (e.g.
			// from OnDisconnect) win the race and overwrite this status
			// with StatusNormalClosure on the wire. Running it inline
			// guarantees this frame is sent, and casClosing latched,
			// before any Handler code can call Close again.
			_ = c.conn.Close(websocket.StatusUnsupportedData, "only binary frames are accepted")
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
