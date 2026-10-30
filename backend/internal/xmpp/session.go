package xmpp

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// session is one authenticated, bound c2s stream. It owns the read loop;
// writes are serialized by wmu.
type session struct {
	conn   net.Conn
	dec    *xml.Decoder
	jid    string
	domain string

	wmu sync.Mutex

	pmu     sync.Mutex
	pending map[string]pendingIQ

	done     chan struct{}
	doneOnce sync.Once
	errMu    sync.Mutex
	closeErr error
}

type pendingIQ struct {
	to string
	ch chan *IQ
}

type streamFeatures struct {
	StartTLS *struct {
		Required *struct{} `xml:"required"`
	} `xml:"urn:ietf:params:xml:ns:xmpp-tls starttls"`
	Mechanisms *struct {
		List []string `xml:"mechanism"`
	} `xml:"urn:ietf:params:xml:ns:xmpp-sasl mechanisms"`
	Bind    *struct{} `xml:"urn:ietf:params:xml:ns:xmpp-bind bind"`
	Session *struct {
		Optional *struct{} `xml:"optional"`
	} `xml:"urn:ietf:params:xml:ns:xmpp-session session"`
}

// connect runs the whole RFC 6120 login: TCP, stream, mandatory STARTTLS,
// SASL, stream restart, resource bind. Any deviation fails the attempt and
// Run retries with backoff.
func (c *Client) connect(ctx context.Context) (*session, error) {
	dialer := net.Dialer{Timeout: c.cfg.DialTimeout, KeepAlive: 30 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", c.cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", c.cfg.Addr, err)
	}
	ok := false
	defer func() {
		if !ok {
			raw.Close()
		}
	}()
	_ = raw.SetDeadline(time.Now().Add(handshakeTimeout))

	var conn net.Conn = raw
	dec, feats, err := c.openStream(conn)
	if err != nil {
		return nil, err
	}
	if feats.StartTLS == nil {
		// Annex K carries the device's Connection Request password inside
		// the stanza; never run that over a plaintext stream.
		return nil, errors.New("server does not offer STARTTLS; refusing an unencrypted XMPP session")
	}
	if _, err := io.WriteString(conn, `<starttls xmlns="`+nsTLS+`"/>`); err != nil {
		return nil, err
	}
	start, err := nextElement(dec)
	if err != nil {
		return nil, fmt.Errorf("starttls: %w", err)
	}
	if start.Name.Local != "proceed" {
		return nil, fmt.Errorf("starttls refused by server (%s)", start.Name.Local)
	}
	tlsConn := tls.Client(raw, c.cfg.TLS)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	conn = tlsConn

	dec, feats, err = c.openStream(conn)
	if err != nil {
		return nil, err
	}
	if feats.Mechanisms == nil {
		return nil, errors.New("server offered no SASL mechanisms after STARTTLS")
	}
	if err := c.authenticate(conn, dec, feats.Mechanisms.List); err != nil {
		return nil, err
	}

	dec, feats, err = c.openStream(conn)
	if err != nil {
		return nil, err
	}
	if feats.Bind == nil {
		return nil, errors.New("server did not offer resource binding")
	}
	jid, err := c.bind(conn, dec)
	if err != nil {
		return nil, err
	}
	if feats.Session != nil && feats.Session.Optional == nil {
		// RFC 3921 session establishment, still mandatory on some servers.
		if err := handshakeIQ(conn, dec, "sess1", `<session xmlns="`+nsSession+`"/>`, nil); err != nil {
			return nil, fmt.Errorf("session establishment: %w", err)
		}
	}
	// Initial presence with negative priority: makes the ACS's session
	// "available" for servers that only route to available resources,
	// while never being chosen for messages addressed to the bare JID.
	if _, err := io.WriteString(conn, `<presence><priority>-1</priority></presence>`); err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})

	s := &session{conn: conn, dec: dec, jid: jid, domain: c.cfg.Domain, pending: map[string]pendingIQ{}, done: make(chan struct{})}
	go s.readLoop(c)
	ok = true
	return s, nil
}

// openStream writes a stream header and reads the server's header plus
// <stream:features/>. A fresh decoder is used per stream (RFC 6120 §4.3.3:
// after STARTTLS and SASL both sides restart the stream from scratch).
func (c *Client) openStream(conn net.Conn) (*xml.Decoder, *streamFeatures, error) {
	header := `<?xml version="1.0"?><stream:stream to="` + xmlAttr(c.cfg.Domain) +
		`" version="1.0" xmlns="jabber:client" xmlns:stream="` + nsStream + `">`
	if _, err := io.WriteString(conn, header); err != nil {
		return nil, nil, fmt.Errorf("write stream header: %w", err)
	}
	dec := xml.NewDecoder(conn)
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("read stream header: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			if se.Name.Space != nsStream || se.Name.Local != "stream" {
				return nil, nil, fmt.Errorf("unexpected stream root %s", se.Name.Local)
			}
			break
		}
	}
	start, err := nextElement(dec)
	if err != nil {
		return nil, nil, fmt.Errorf("read stream features: %w", err)
	}
	if start.Name.Space == nsStream && start.Name.Local == "error" {
		return nil, nil, readStreamError(dec, &start)
	}
	if start.Name.Local != "features" {
		return nil, nil, fmt.Errorf("expected stream features, got %s", start.Name.Local)
	}
	var feats streamFeatures
	if err := dec.DecodeElement(&feats, &start); err != nil {
		return nil, nil, fmt.Errorf("parse stream features: %w", err)
	}
	return dec, &feats, nil
}

type saslElement struct {
	XMLName xml.Name
	Text    string `xml:",chardata"`
	Inner   []byte `xml:",innerxml"`
}

// authenticate runs one SASL exchange. Failure details from the server
// (e.g. not-authorized) are reported; the password never is.
func (c *Client) authenticate(conn net.Conn, dec *xml.Decoder, offered []string) error {
	mech := chooseMechanism(offered)
	if mech == "" {
		return fmt.Errorf("no supported SASL mechanism (server offers %s)", strings.Join(offered, ", "))
	}
	var scram *scramClient
	var initial []byte
	if mech == mechPLAIN {
		initial = plainInitial(c.cfg.Username, c.cfg.Password)
	} else {
		var err error
		if scram, err = newSCRAM(mech, c.cfg.Username, c.cfg.Password); err != nil {
			return err
		}
		initial = scram.first()
	}
	if _, err := io.WriteString(conn, `<auth xmlns="`+nsSASL+`" mechanism="`+mech+`">`+base64.StdEncoding.EncodeToString(initial)+`</auth>`); err != nil {
		return err
	}
	step := 0
	for {
		start, err := nextElement(dec)
		if err != nil {
			return fmt.Errorf("sasl: %w", err)
		}
		var el saslElement
		if err := dec.DecodeElement(&el, &start); err != nil {
			return fmt.Errorf("sasl: %w", err)
		}
		data, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(el.Text))
		switch start.Name.Local {
		case "challenge":
			if scram == nil {
				return errors.New("sasl: unexpected challenge for PLAIN")
			}
			var reply []byte
			switch step {
			case 0:
				if reply, err = scram.final(data); err != nil {
					return err
				}
			default:
				// Some servers send server-final as a challenge and
				// expect an empty response before <success/>.
				if err := scram.verify(data); err != nil {
					return err
				}
				scram = nil // verified; <success/> may now be empty
			}
			step++
			if _, err := io.WriteString(conn, `<response xmlns="`+nsSASL+`">`+base64.StdEncoding.EncodeToString(reply)+`</response>`); err != nil {
				return err
			}
		case "success":
			if scram != nil {
				if err := scram.verify(data); err != nil {
					return err
				}
			}
			return nil
		case "failure":
			cond := "failure"
			if name, ok := firstChild(el.Inner); ok {
				cond = name.Local
			}
			return fmt.Errorf("sasl %s authentication failed: %s", mech, cond)
		default:
			return fmt.Errorf("sasl: unexpected element %s", start.Name.Local)
		}
	}
}

// bind requests the configured resource and returns the full JID the server
// assigned (which may differ: servers are allowed to override it).
func (c *Client) bind(conn net.Conn, dec *xml.Decoder) (string, error) {
	type bindReq struct {
		XMLName  xml.Name `xml:"urn:ietf:params:xml:ns:xmpp-bind bind"`
		Resource string   `xml:"resource"`
	}
	payload, err := xml.Marshal(bindReq{Resource: c.cfg.Resource})
	if err != nil {
		return "", err
	}
	var result struct {
		JID string `xml:"jid"`
	}
	if err := handshakeIQ(conn, dec, "bind1", string(payload), &result); err != nil {
		return "", fmt.Errorf("resource bind: %w", err)
	}
	if result.JID == "" {
		return "", errors.New("resource bind: server returned no jid")
	}
	return result.JID, nil
}

// handshakeIQ performs one synchronous iq set before the read loop exists
// (bind, legacy session). into, when non-nil, receives the result's first
// child element.
func handshakeIQ(conn net.Conn, dec *xml.Decoder, id, payload string, into any) error {
	if _, err := io.WriteString(conn, `<iq type="set" id="`+id+`">`+payload+`</iq>`); err != nil {
		return err
	}
	for {
		start, err := nextElement(dec)
		if err != nil {
			return err
		}
		if start.Name.Local != "iq" {
			if err := dec.Skip(); err != nil {
				return err
			}
			continue
		}
		var iq IQ
		if err := dec.DecodeElement(&iq, &start); err != nil {
			return err
		}
		if iq.ID != id {
			continue
		}
		if iq.Type == "error" {
			return ParseStanzaError(iq.Payload)
		}
		if into != nil {
			return xml.Unmarshal(iq.Payload, into)
		}
		return nil
	}
}

// readLoop dispatches everything the server sends until the stream ends.
func (s *session) readLoop(c *Client) {
	for {
		start, err := nextElement(s.dec)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errors.New("server closed the stream")
			}
			s.close(err)
			return
		}
		switch {
		case start.Name.Space == nsStream && start.Name.Local == "error":
			s.close(readStreamError(s.dec, &start))
			return
		case start.Name.Local == "iq":
			var iq IQ
			if err := s.dec.DecodeElement(&iq, &start); err != nil {
				s.close(fmt.Errorf("decode iq: %w", err))
				return
			}
			s.handleIQ(c, &iq)
		default:
			// message/presence: nothing to do for a Connection Request client.
			if err := s.dec.Skip(); err != nil {
				s.close(err)
				return
			}
		}
	}
}

func (s *session) handleIQ(c *Client, iq *IQ) {
	switch iq.Type {
	case "result", "error":
		s.pmu.Lock()
		p, ok := s.pending[iq.ID]
		if ok && fromMatches(p.to, iq.From, s.domain, s.jid) {
			delete(s.pending, iq.ID)
		} else {
			ok = false
		}
		s.pmu.Unlock()
		if ok {
			p.ch <- iq
		} else {
			c.logger.Debug("xmpp: dropped unmatched iq response", "id", iq.ID, "from", iq.From)
		}
	case "get", "set":
		var reply []byte
		var err error
		if name, ok := firstChild(iq.Payload); ok && iq.Type == "get" && name.Space == nsPing && name.Local == "ping" {
			reply, err = (&IQ{ID: iq.ID, Type: "result", To: iq.From}).Marshal()
		} else {
			reply, err = errorReply(iq, "cancel", "service-unavailable")
		}
		if err == nil {
			_ = s.write(reply)
		}
	}
}

// roundTrip writes a request and waits for its response.
func (s *session) roundTrip(ctx context.Context, id, to string, raw []byte) (*IQ, error) {
	ch := make(chan *IQ, 1)
	s.pmu.Lock()
	s.pending[id] = pendingIQ{to: to, ch: ch}
	s.pmu.Unlock()
	defer func() {
		s.pmu.Lock()
		delete(s.pending, id)
		s.pmu.Unlock()
	}()
	if err := s.write(raw); err != nil {
		return nil, err
	}
	select {
	case iq := <-ch:
		if iq.Type == "error" {
			return iq, ParseStanzaError(iq.Payload)
		}
		return iq, nil
	case <-s.done:
		return nil, fmt.Errorf("%w: %v", ErrNotConnected, s.err())
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *session) write(b []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	select {
	case <-s.done:
		return ErrNotConnected
	default:
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := s.conn.Write(b)
	if err != nil {
		go s.close(fmt.Errorf("write: %w", err))
	}
	return err
}

func (s *session) close(err error) {
	s.doneOnce.Do(func() {
		s.errMu.Lock()
		s.closeErr = err
		s.errMu.Unlock()
		close(s.done)
		// Best-effort polite close; skipped when a writer is mid-write
		// (likely the very write that failed) rather than interleaving.
		if s.wmu.TryLock() {
			_ = s.conn.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(s.conn, `</stream:stream>`)
			s.wmu.Unlock()
		}
		_ = s.conn.Close()
	})
}

func (s *session) err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.closeErr == nil {
		return errors.New("session closed")
	}
	return s.closeErr
}

func xmlAttr(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return strings.ReplaceAll(b.String(), `"`, "&#34;")
}
