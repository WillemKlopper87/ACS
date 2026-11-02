package xmpp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func xmlUnmarshal(b []byte, v any) error { return xml.Unmarshal(b, v) }

const testDomain = "xmpp.test"

// fakeServer is a minimal in-process c2s server: mandatory STARTTLS, SASL
// PLAIN, resource bind, XEP-0199 ping, the two XEP-0133 commands the
// client uses, and routing of iqs to fake CPEs by full JID. It exists to
// exercise the real client code paths end to end without a Prosody
// container; it is not a general XMPP server.
type fakeServer struct {
	t        *testing.T
	ln       net.Listener
	tlsCfg   *tls.Config
	noTLS    atomic.Bool       // set by a test while serve runs
	users    map[string]string // localpart -> password
	mu       sync.Mutex
	conns    []net.Conn
	cpes     map[string]func(user, pass string) *StanzaError // full JID -> handler
	received []string                                        // connection request credentials seen, "user:pass"
}

func newFakeServer(t *testing.T) (*fakeServer, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: testDomain}, DNSNames: []string{testDomain},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{
		t: t, ln: ln,
		tlsCfg: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}},
		users:  map[string]string{"acs": "acs-password"},
		cpes:   map[string]func(string, string) *StanzaError{},
	}
	t.Cleanup(func() { ln.Close(); s.dropAll() })
	go s.serve()
	return s, pool
}

func (s *fakeServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		go s.handle(c)
	}
}

func (s *fakeServer) dropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
	s.conns = nil
}

func (s *fakeServer) openStream(conn net.Conn, features string) *xml.Decoder {
	dec := xml.NewDecoder(conn)
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "stream" {
			break
		}
	}
	fmt.Fprintf(conn, `<?xml version='1.0'?><stream:stream xmlns='jabber:client' xmlns:stream='%s' from='%s' id='s1' version='1.0'><stream:features>%s</stream:features>`, nsStream, testDomain, features)
	return dec
}

func (s *fakeServer) handle(raw net.Conn) {
	defer raw.Close()
	var conn net.Conn = raw
	if s.noTLS.Load() {
		s.openStream(conn, `<mechanisms xmlns='`+nsSASL+`'><mechanism>PLAIN</mechanism></mechanisms>`)
		io.Copy(io.Discard, conn)
		return
	}
	dec := s.openStream(conn, `<starttls xmlns='`+nsTLS+`'><required/></starttls>`)
	if dec == nil {
		return
	}
	if start, err := nextElement(dec); err != nil || start.Name.Local != "starttls" {
		return
	}
	fmt.Fprintf(conn, `<proceed xmlns='%s'/>`, nsTLS)
	tlsConn := tls.Server(raw, s.tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	conn = tlsConn

	dec = s.openStream(conn, `<mechanisms xmlns='`+nsSASL+`'><mechanism>PLAIN</mechanism></mechanisms>`)
	start, err := nextElement(dec)
	if err != nil || start.Name.Local != "auth" {
		return
	}
	var auth struct {
		Mechanism string `xml:"mechanism,attr"`
		Text      string `xml:",chardata"`
	}
	dec.DecodeElement(&auth, &start)
	decoded, _ := base64.StdEncoding.DecodeString(auth.Text)
	parts := strings.Split(string(decoded), "\x00")
	if auth.Mechanism != "PLAIN" || len(parts) != 3 {
		fmt.Fprintf(conn, `<failure xmlns='%s'><not-authorized/></failure>`, nsSASL)
		return
	}
	user := parts[1]
	s.mu.Lock()
	known := len(parts) == 3 && s.users[user] != "" && s.users[user] == parts[2]
	s.mu.Unlock()
	if !known {
		fmt.Fprintf(conn, `<failure xmlns='%s'><not-authorized/></failure>`, nsSASL)
		return
	}
	fmt.Fprintf(conn, `<success xmlns='%s'/>`, nsSASL)

	dec = s.openStream(conn, `<bind xmlns='`+nsBind+`'/><session xmlns='`+nsSession+`'><optional/></session>`)
	var full string
	for {
		start, err := nextElement(dec)
		if err != nil {
			return
		}
		if start.Name.Local != "iq" {
			dec.Skip()
			continue
		}
		var iq IQ
		if err := dec.DecodeElement(&iq, &start); err != nil {
			return
		}
		if full == "" {
			var b struct {
				Resource string `xml:"resource"`
			}
			xml.Unmarshal(iq.Payload, &b)
			full = user + "@" + testDomain + "/" + b.Resource
			fmt.Fprintf(conn, `<iq type='result' id='%s'><bind xmlns='%s'><jid>%s</jid></bind></iq>`, iq.ID, nsBind, full)
			continue
		}
		s.route(conn, full, &iq)
	}
}

func (s *fakeServer) route(conn net.Conn, from string, iq *IQ) {
	reply := func(typ, to, payload string) {
		fmt.Fprintf(conn, `<iq type='%s' id='%s' from='%s' to='%s'>%s</iq>`, typ, iq.ID, to, from, payload)
	}
	name, _ := firstChild(iq.Payload)
	switch {
	case iq.To == testDomain && name.Local == "ping":
		reply("result", testDomain, "")
	case iq.To == testDomain && name.Space == nsCommands:
		var cmd adhocCommand
		xml.Unmarshal(iq.Payload, &cmd)
		if cmd.Form == nil {
			reply("result", testDomain, fmt.Sprintf(`<command xmlns='%s' node='%s' sessionid='sess-1' status='executing'><x xmlns='%s' type='form'/></command>`, nsCommands, cmd.Node, nsData))
			return
		}
		fields := map[string]string{}
		for _, f := range cmd.Form.Fields {
			if len(f.Values) > 0 {
				fields[f.Var] = f.Values[0]
			}
		}
		local := strings.TrimSuffix(fields["accountjid"], "@"+testDomain)
		note := `<note type='info'>ok</note>`
		s.mu.Lock()
		switch {
		case cmd.SessionID != "sess-1" || fields["FORM_TYPE"] != formTypeAdmin:
			note = `<note type='error'>bad form</note>`
		case cmd.Node == nodeAddUser && s.users[local] != "":
			note = `<note type='error'>Account already exists</note>`
		case cmd.Node == nodeAddUser:
			s.users[local] = fields["password"]
		case cmd.Node == nodeChangePassword && s.users[local] != "":
			s.users[local] = fields["password"]
		default:
			note = `<note type='error'>User does not exist</note>`
		}
		s.mu.Unlock()
		reply("result", testDomain, fmt.Sprintf(`<command xmlns='%s' node='%s' sessionid='sess-1' status='completed'>%s</command>`, nsCommands, cmd.Node, note))
	default:
		s.mu.Lock()
		handler := s.cpes[iq.To]
		s.mu.Unlock()
		if handler == nil {
			reply("error", iq.To, `<error type='cancel'><service-unavailable xmlns='`+nsStanzas+`'/></error>`)
			return
		}
		user, pass, ok := ParseConnectionRequest(iq.Payload)
		if !ok {
			reply("error", iq.To, `<error type='cancel'><feature-not-implemented xmlns='`+nsStanzas+`'/></error>`)
			return
		}
		s.mu.Lock()
		s.received = append(s.received, user+":"+pass)
		s.mu.Unlock()
		if serr := handler(user, pass); serr != nil {
			reply("error", iq.To, `<error type='`+serr.Type+`'><`+serr.Condition+` xmlns='`+nsStanzas+`'/></error>`)
			return
		}
		reply("result", iq.To, "")
	}
}

func startClient(t *testing.T, s *fakeServer, pool *x509.CertPool, password string) (*Client, context.CancelFunc) {
	t.Helper()
	c, err := New(Config{
		Addr: s.ln.Addr().String(), Domain: testDomain, Username: "acs", Password: password,
		Resource: "acs", TLS: &tls.Config{RootCAs: pool}, KeepAlive: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	t.Cleanup(cancel)
	return c, cancel
}

func waitConnected(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !c.Connected() {
		if time.Now().After(deadline) {
			t.Fatalf("client never connected: %+v", c.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestClientConnectionRequestEndToEnd drives the whole client against the
// fake server: STARTTLS with a pinned self-signed certificate, SASL,
// bind, then an Annex K Connection Request a fake CPE accepts.
func TestClientConnectionRequestEndToEnd(t *testing.T) {
	s, pool := newFakeServer(t)
	cpe := "xmpp-dev1@" + testDomain + "/cwmp"
	s.cpes[cpe] = func(user, pass string) *StanzaError {
		if user != "cr-user" || pass != "cr-pass" {
			return &StanzaError{Type: "auth", Condition: "not-authorized"}
		}
		return nil
	}
	c, _ := startClient(t, s, pool, "acs-password")
	waitConnected(t, c)
	if st := c.Status(); st.JID != "acs@"+testDomain+"/acs" || st.ConnectedSince == nil {
		t.Fatalf("status = %+v", st)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.ConnectionRequest(ctx, cpe, "cr-user", "cr-pass"); err != nil {
		t.Fatalf("ConnectionRequest: %v", err)
	}
	err := c.ConnectionRequest(ctx, cpe, "cr-user", "wrong")
	var se *StanzaError
	if !errors.As(err, &se) || se.Condition != "not-authorized" {
		t.Fatalf("bad credentials: err = %v, want not-authorized stanza error", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("the password leaked into the error text")
	}
	// An offline resource: the server answers service-unavailable on the
	// CPE's behalf.
	err = c.ConnectionRequest(ctx, "xmpp-offline@"+testDomain+"/cwmp", "u", "p")
	if !errors.As(err, &se) || se.Condition != "service-unavailable" {
		t.Fatalf("offline CPE: err = %v, want service-unavailable", err)
	}
	if err := c.ConnectionRequest(ctx, "bare@"+testDomain, "u", "p"); !errors.Is(err, ErrInvalidJID) {
		t.Fatalf("bare JID: err = %v, want ErrInvalidJID", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.received) != 2 || s.received[0] != "cr-user:cr-pass" {
		t.Fatalf("CPE received %v", s.received)
	}
}

// TestClientReconnectsAfterServerDrop proves the session comes back on its
// own after the server (or a NAT in between) kills the TCP connection.
func TestClientReconnectsAfterServerDrop(t *testing.T) {
	s, pool := newFakeServer(t)
	c, _ := startClient(t, s, pool, "acs-password")
	waitConnected(t, c)
	s.dropAll()
	deadline := time.Now().Add(3 * time.Second)
	for c.Connected() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	waitConnected(t, c) // backoff starts at ~1 s
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.SendIQ(ctx, "get", testDomain, []byte(`<ping xmlns="urn:xmpp:ping"/>`)); err != nil {
		t.Fatalf("ping after reconnect: %v", err)
	}
}

func TestClientNotConnected(t *testing.T) {
	c, err := New(Config{Addr: "127.0.0.1:1", Domain: testDomain, Username: "acs", Password: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ConnectionRequest(context.Background(), "a@b/c", "u", "p"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
	if _, err := New(Config{Addr: "x"}); err == nil {
		t.Fatal("incomplete config must be rejected")
	}
}

func TestClientRejectsBadPasswordAndPlaintextServers(t *testing.T) {
	s, pool := newFakeServer(t)
	c, err := New(Config{Addr: s.ln.Addr().String(), Domain: testDomain, Username: "acs", Password: "wrong", TLS: &tls.Config{RootCAs: pool}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.connect(context.Background()); err == nil || !strings.Contains(err.Error(), "not-authorized") {
		t.Fatalf("bad password: err = %v", err)
	}

	plain, _ := newFakeServer(t)
	plain.noTLS.Store(true)
	c2, _ := New(Config{Addr: plain.ln.Addr().String(), Domain: testDomain, Username: "acs", Password: "acs-password"})
	if _, err := c2.connect(context.Background()); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("plaintext server: err = %v, want STARTTLS refusal", err)
	}

	// An unpinned self-signed certificate must fail verification.
	c3, _ := New(Config{Addr: s.ln.Addr().String(), Domain: testDomain, Username: "acs", Password: "acs-password"})
	if _, err := c3.connect(context.Background()); err == nil || !strings.Contains(err.Error(), "tls") {
		t.Fatalf("unpinned cert: err = %v, want a TLS failure", err)
	}
}

// TestEnsureAccountIsIdempotent covers the XEP-0133 path the provisioning
// flow uses: the first call creates the account, a retry with a new
// password resets it instead of failing on "Account already exists".
func TestEnsureAccountIsIdempotent(t *testing.T) {
	s, pool := newFakeServer(t)
	c, _ := startClient(t, s, pool, "acs-password")
	waitConnected(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	jid := "xmpp-dev1@" + testDomain
	if err := c.EnsureAccount(ctx, jid, "first"); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureAccount(ctx, jid, "second"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	got := s.users["xmpp-dev1"]
	s.mu.Unlock()
	if got != "second" {
		t.Fatalf("password = %q, want the second one", got)
	}
	if err := c.EnsureAccount(ctx, "xmpp-dev1@elsewhere", "x"); !errors.Is(err, ErrInvalidJID) {
		t.Fatalf("foreign domain: err = %v", err)
	}
	if err := c.EnsureAccount(ctx, jid+"/res", "x"); !errors.Is(err, ErrInvalidJID) {
		t.Fatalf("full JID: err = %v", err)
	}
}
