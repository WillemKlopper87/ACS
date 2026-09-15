package mtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"time"

	"acs/internal/usp"
	"acs/internal/usp/principal"
)

const principalAuthTimeout = 5 * time.Second

var (
	errClientCertificateRequired = errors.New("mtp: authenticated USP principal requires a verified client certificate")
	errEndpointIDMismatch        = errors.New("mtp: claimed USP EndpointID does not match authenticated principal")
	errMQTTReplyTopicMismatch    = errors.New("mtp: MQTT reply topic does not match authenticated principal")
)

// peerCertificate returns the verified leaf certificate carried by a TLS
// connection. crypto/tls populates PeerCertificates after the handshake; the
// caller's TLS config is responsible for chain verification before this point.
func peerCertificate(conn net.Conn) *x509.Certificate {
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return nil
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil
	}
	return state.PeerCertificates[0]
}

func authenticateCertificate(ctx context.Context, auth principal.CertificateAuthenticator, cert *x509.Certificate) (*principal.Principal, error) {
	if auth == nil {
		return nil, nil
	}
	if cert == nil {
		return nil, errClientCertificateRequired
	}
	authCtx, cancel := context.WithTimeout(ctx, principalAuthTimeout)
	defer cancel()
	return auth.AuthenticateCertificate(authCtx, cert)
}

func verifyEndpointPrincipal(p *principal.Principal, claimed usp.EndpointID) error {
	if p == nil {
		return nil // lab profile: no cryptographic principal configured
	}
	if p.EndpointID != claimed {
		return errEndpointIDMismatch
	}
	return nil
}

func verifyMQTTReplyTopic(p *principal.Principal, replyTopic string) error {
	if p == nil {
		return nil // lab profile
	}
	if p.MQTTTopic != replyTopic {
		return errMQTTReplyTopicMismatch
	}
	return nil
}

// mqttTopicAllowed is the production MQTT ACL derived from the authenticated
// principal. An agent may publish only to the controller topic (MQTT 5) or to
// the controller's MQTT 3.1.1 reply-to form naming its own response topic. It
// may read only its exact response topic or descendants below that root (the
// v3.1.1 controller reply carries its own reply-to suffix there).
func mqttTopicAllowed(p *principal.Principal, controllerTopic, topic string, write bool) bool {
	if p == nil {
		return false
	}
	if write {
		if topic == controllerTopic {
			return true
		}
		return topic == controllerTopic+replyToKey+EscapeReplyTo(p.MQTTTopic)
	}
	if topic == p.MQTTTopic || topic == p.MQTTTopic+"/#" {
		return true
	}
	return strings.HasPrefix(topic, p.MQTTTopic+"/") && !strings.ContainsAny(topic, "+#")
}
