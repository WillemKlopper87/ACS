package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"acs/internal/xmpp"
)

// xmppSettings is the ACS-side XMPP configuration: how the ACS reaches its
// own server (the client), and what provisioning tells CPEs (where to
// connect, which resource, which keepalive).
type xmppSettings struct {
	client *xmpp.Client

	// What zero-touch provisioning writes into Device.XMPP.Connection.{i}.
	cpeServerAddress string // public address CPEs dial (ACS_XMPP_CPE_SERVER_ADDRESS)
	cpeServerPort    int
	cpeResource      string
	cpeKeepAlive     int // seconds; Device.XMPP.Connection.{i}.KeepAliveInterval
}

// xmppFromEnv builds the XMPP client from ACS_XMPP_*. It returns (nil, nil)
// when ACS_XMPP_SERVER is unset — XMPP Connection Request is then off and
// nothing else in the API changes. A partial configuration is an error
// rather than a silent disable, so a typo cannot quietly cost every NAT'd
// device its fast path.
//
//	ACS_XMPP_SERVER            host[:port] the ACS dials (default port 5222)
//	ACS_XMPP_JID               ACS account, localpart@domain (bare)
//	ACS_XMPP_DOMAIN            XMPP domain (defaults to the JID's domain)
//	ACS_XMPP_PASSWORD          ACS account password
//	ACS_XMPP_RESOURCE          ACS resource (default "acs")
//	ACS_XMPP_TLS_CA_FILE       PEM trust anchors for the server cert (e.g. the
//	                           lab's self-signed cert — pinning, not skipping)
//	ACS_XMPP_TLS_SERVER_NAME   name verified in the cert (default: domain)
//	ACS_XMPP_TLS_INSECURE_SKIP_VERIFY=true  lab escape hatch, logged loudly
//	ACS_XMPP_KEEPALIVE         ping interval to the server (default 60s)
//	ACS_XMPP_CPE_SERVER_ADDRESS / _PORT / _RESOURCE / _KEEPALIVE_SECONDS
//	                           what provisioning configures on the CPE
func xmppFromEnv(logger *slog.Logger) (*xmppSettings, error) {
	server := strings.TrimSpace(os.Getenv("ACS_XMPP_SERVER"))
	if server == "" {
		return nil, nil
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "5222")
	}
	jid := strings.TrimSpace(os.Getenv("ACS_XMPP_JID"))
	password := os.Getenv("ACS_XMPP_PASSWORD")
	domain := strings.TrimSpace(os.Getenv("ACS_XMPP_DOMAIN"))
	at := strings.IndexByte(jid, '@')
	if at <= 0 || strings.Contains(jid, "/") {
		return nil, errors.New("ACS_XMPP_JID must be a bare JID (localpart@domain)")
	}
	if domain == "" {
		domain = jid[at+1:]
	}
	if !strings.EqualFold(jid[at+1:], domain) {
		return nil, fmt.Errorf("ACS_XMPP_JID domain %q does not match ACS_XMPP_DOMAIN %q", jid[at+1:], domain)
	}
	if len(password) < 16 {
		return nil, errors.New("ACS_XMPP_PASSWORD must be set (at least 16 bytes)")
	}

	tlsCfg := &tls.Config{ServerName: envOr("ACS_XMPP_TLS_SERVER_NAME", domain)}
	if caFile := os.Getenv("ACS_XMPP_TLS_CA_FILE"); caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read ACS_XMPP_TLS_CA_FILE: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ACS_XMPP_TLS_CA_FILE contains no PEM certificates")
		}
		tlsCfg.RootCAs = pool
	}
	if os.Getenv("ACS_XMPP_TLS_INSECURE_SKIP_VERIFY") == "true" {
		logger.Warn("ACS_XMPP_TLS_INSECURE_SKIP_VERIFY=true — the ACS does not verify its XMPP server's certificate; device Connection Request passwords could be intercepted. Pin the server cert with ACS_XMPP_TLS_CA_FILE instead.")
		tlsCfg.InsecureSkipVerify = true
	}
	keepAlive := 60 * time.Second
	if v := os.Getenv("ACS_XMPP_KEEPALIVE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 5*time.Second {
			return nil, fmt.Errorf("invalid ACS_XMPP_KEEPALIVE %q (a duration of at least 5s)", v)
		}
		keepAlive = d
	}

	client, err := xmpp.New(xmpp.Config{
		Addr: server, Domain: domain, Username: jid[:at], Password: password,
		Resource: envOr("ACS_XMPP_RESOURCE", "acs"), TLS: tlsCfg, KeepAlive: keepAlive, Logger: logger,
	})
	if err != nil {
		return nil, err
	}

	s := &xmppSettings{
		client:           client,
		cpeServerAddress: strings.TrimSpace(os.Getenv("ACS_XMPP_CPE_SERVER_ADDRESS")),
		cpeServerPort:    5222,
		cpeResource:      envOr("ACS_XMPP_CPE_RESOURCE", "cwmp"),
		cpeKeepAlive:     60,
	}
	if v := os.Getenv("ACS_XMPP_CPE_SERVER_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid ACS_XMPP_CPE_SERVER_PORT %q", v)
		}
		s.cpeServerPort = n
	}
	if v := os.Getenv("ACS_XMPP_CPE_KEEPALIVE_SECONDS"); v != "" {
		// TR-181 KeepAliveInterval: -1 disables keepalives; otherwise
		// seconds. CGNAT idle timers for TCP are often minutes, sometimes
		// less, so 60 s is the default rather than a CPE's typical -1.
		n, err := strconv.Atoi(v)
		if err != nil || n < -1 || n == 0 || n > 86400 {
			return nil, fmt.Errorf("invalid ACS_XMPP_CPE_KEEPALIVE_SECONDS %q (-1 or 1..86400)", v)
		}
		s.cpeKeepAlive = n
	}
	if s.cpeServerAddress == "" {
		logger.Warn("ACS_XMPP_CPE_SERVER_ADDRESS not set — the ACS's XMPP client runs, but zero-touch XMPP provisioning is refused until the public address CPEs should dial is configured")
	}
	return s, nil
}
