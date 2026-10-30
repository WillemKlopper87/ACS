package xmpp

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNotConnected means the ACS currently has no authenticated session to
// its XMPP server (still connecting, or reconnecting after a failure).
// The Connection Request worker treats it as XMPP_UNAVAILABLE and falls
// back to its HTTP/Annex G/periodic behaviour.
var ErrNotConnected = errors.New("xmpp: not connected")

const (
	defaultKeepAlive   = 60 * time.Second
	defaultDialTimeout = 10 * time.Second
	handshakeTimeout   = 20 * time.Second
	pingTimeout        = 15 * time.Second
	writeTimeout       = 10 * time.Second
	minBackoff         = 1 * time.Second
	maxBackoff         = 60 * time.Second
)

// Config configures the ACS's XMPP client. Addr, Domain, Username and
// Password are required.
type Config struct {
	// Addr is the server's host:port the ACS dials (ACS_XMPP_SERVER) — on
	// the lab host that is the Prosody container on 127.0.0.1:5222.
	Addr string
	// Domain is the XMPP domain (the part after "@" in every JID).
	Domain string
	// Username is the ACS account's localpart; Password its secret.
	Username string
	Password string
	// Resource is requested at bind time. A stable resource keeps the ACS's
	// full JID stable, which matters to CPEs that compare the full "from"
	// against ConnReqAllowedJabberIDs.
	Resource string
	// TLS configures STARTTLS. ServerName defaults to Domain. A nil config
	// verifies against the system roots.
	TLS *tls.Config
	// KeepAlive is the interval between XEP-0199 pings to the server; a
	// ping that goes unanswered tears the session down and reconnects.
	KeepAlive   time.Duration
	DialTimeout time.Duration
	Logger      *slog.Logger
}

// Status is a point-in-time view of the client for the operator API.
type Status struct {
	Connected      bool       `json:"connected"`
	JID            string     `json:"jid"`
	ConnectedSince *time.Time `json:"connected_since,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
}

// Client maintains one long-lived, auto-reconnecting session. Safe for
// concurrent use; Run must be started for it to ever connect.
type Client struct {
	cfg      Config
	logger   *slog.Logger
	idPrefix string
	seq      atomic.Uint64

	mu        sync.Mutex
	sess      *session
	since     time.Time
	lastError string
}

// New validates cfg and returns an unconnected client.
func New(cfg Config) (*Client, error) {
	if cfg.Addr == "" || cfg.Domain == "" || cfg.Username == "" || cfg.Password == "" {
		return nil, errors.New("xmpp: Addr, Domain, Username and Password are required")
	}
	if cfg.Resource == "" {
		cfg.Resource = "acs"
	}
	if cfg.KeepAlive <= 0 {
		cfg.KeepAlive = defaultKeepAlive
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultDialTimeout
	}
	if cfg.TLS == nil {
		cfg.TLS = &tls.Config{}
	} else {
		cfg.TLS = cfg.TLS.Clone()
	}
	if cfg.TLS.ServerName == "" {
		cfg.TLS.ServerName = cfg.Domain
	}
	if cfg.TLS.MinVersion == 0 {
		cfg.TLS.MinVersion = tls.VersionTLS12
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	prefix := make([]byte, 6)
	if _, err := rand.Read(prefix); err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, logger: logger, idPrefix: "acs" + hex.EncodeToString(prefix) + "-"}, nil
}

// BareJID is the ACS account's bare JID — the value provisioning writes to
// a CPE's ManagementServer.ConnReqAllowedJabberIDs.
func (c *Client) BareJID() string { return c.cfg.Username + "@" + c.cfg.Domain }

// Domain is the XMPP domain this client authenticates against.
func (c *Client) Domain() string { return c.cfg.Domain }

// Connected reports whether an authenticated session is up right now.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess != nil
}

// Status reports connection state for the operator API.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Status{Connected: c.sess != nil, JID: c.BareJID(), LastError: c.lastError}
	if c.sess != nil {
		st.JID = c.sess.jid
		since := c.since
		st.ConnectedSince = &since
	}
	return st
}

// Run connects and keeps the session alive until ctx is cancelled,
// reconnecting with jittered exponential backoff (1 s doubling to 60 s,
// reset after every successful login).
func (c *Client) Run(ctx context.Context) {
	backoff := minBackoff
	for ctx.Err() == nil {
		sess, err := c.connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.setError(err)
			delay := jitter(backoff)
			c.logger.Warn("xmpp connect failed; retrying", "err", err, "server", c.cfg.Addr, "retry_in", delay.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = minBackoff
		c.mu.Lock()
		c.sess, c.since, c.lastError = sess, time.Now().UTC(), ""
		c.mu.Unlock()
		c.logger.Info("xmpp session established", "jid", sess.jid, "server", c.cfg.Addr)

		err = c.keepAlive(ctx, sess)

		c.mu.Lock()
		c.sess = nil
		c.mu.Unlock()
		sess.close(err)
		if ctx.Err() != nil {
			return
		}
		c.setError(err)
		c.logger.Warn("xmpp session lost; reconnecting", "err", err)
	}
}

func (c *Client) setError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.lastError = err.Error()
	c.mu.Unlock()
}

// keepAlive pings the server every KeepAlive until the session dies, a
// ping goes unanswered, or ctx ends. An unanswered ping is the only
// reliable signal for a half-open TCP connection (a NAT or firewall on the
// path silently dropping state), which otherwise looks healthy until the
// next Connection Request times out against it.
func (c *Client) keepAlive(ctx context.Context, s *session) error {
	ticker := time.NewTicker(c.cfg.KeepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return s.err()
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(ctx, pingTimeout)
			_, err := c.sendIQ(pctx, s, "get", c.cfg.Domain, []byte(`<ping xmlns="`+nsPing+`"/>`))
			cancel()
			var se *StanzaError
			if err != nil && !errors.As(err, &se) { // any answer, even an error, proves liveness
				return fmt.Errorf("keepalive ping failed: %w", err)
			}
		}
	}
}

func jitter(d time.Duration) time.Duration {
	// ±20 % so a fleet of ACS replicas restarted together does not hammer
	// the server in lockstep.
	span := int64(d) * 2 / 5
	if span <= 0 {
		return d
	}
	n, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		return d
	}
	return d - time.Duration(span/2) + time.Duration(n.Int64())
}

func (c *Client) nextID() string {
	return c.idPrefix + strconv.FormatUint(c.seq.Add(1), 10)
}

// SendIQ sends an iq get/set and waits for the matching result or error
// (matched by id and sender), bounded by ctx. An error-type reply is
// returned as both the stanza and a *StanzaError.
func (c *Client) SendIQ(ctx context.Context, iqType, to string, payload []byte) (*IQ, error) {
	c.mu.Lock()
	s := c.sess
	c.mu.Unlock()
	if s == nil {
		return nil, ErrNotConnected
	}
	return c.sendIQ(ctx, s, iqType, to, payload)
}

func (c *Client) sendIQ(ctx context.Context, s *session, iqType, to string, payload []byte) (*IQ, error) {
	id := c.nextID()
	raw, err := (&IQ{ID: id, Type: iqType, To: to, Payload: payload}).Marshal()
	if err != nil {
		return nil, err
	}
	return s.roundTrip(ctx, id, to, raw)
}

// ConnectionRequest sends the TR-069 Annex K Connection Request to a CPE's
// full JID and waits for its iq result. nil means the CPE accepted it; the
// CWMP session it then opens is confirmed separately (Inform event 6).
// The credentials never appear in an error or log message.
func (c *Client) ConnectionRequest(ctx context.Context, to, username, password string) error {
	if err := ValidateFullJID(to); err != nil {
		return err
	}
	c.mu.Lock()
	s := c.sess
	c.mu.Unlock()
	if s == nil {
		return ErrNotConnected
	}
	id := c.nextID()
	raw, err := BuildConnectionRequest(id, to, username, password)
	if err != nil {
		return err
	}
	_, err = s.roundTrip(ctx, id, to, raw)
	return err
}

// fromMatches decides whether a reply's "from" is acceptable for a request
// sent "to" a given address (RFC 6120 §8.1.2.1 / §10.3.3). Requests to a
// full JID (a CPE) must be answered by exactly that JID — the server
// stamps "from" on everything it routes, so another account on the server
// cannot forge it. Requests to the server itself (bind, pings, admin
// commands) may be answered with the domain, our own JID, or no "from".
func fromMatches(expectedTo, from, domain, ownJID string) bool {
	if expectedTo == "" || strings.EqualFold(expectedTo, domain) {
		return from == "" || strings.EqualFold(from, domain) ||
			strings.EqualFold(from, ownJID) || strings.EqualFold(from, Bare(ownJID))
	}
	return strings.EqualFold(from, expectedTo)
}
