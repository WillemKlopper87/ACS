package mtp

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"

	"acs/internal/usp"
	"acs/internal/usp/principal"
)

type staticPrincipalAuth struct {
	p   *principal.Principal
	err error
}

func (a staticPrincipalAuth) AuthenticateCertificate(context.Context, *x509.Certificate) (*principal.Principal, error) {
	return a.p, a.err
}

func TestAuthenticatedWebSocketRequiresControllerEndpointID(t *testing.T) {
	_, err := NewWebSocket(WebSocketConfig{
		Addr:                   "127.0.0.1:0",
		AllowPlaintext:         true,
		PrincipalAuthenticator: staticPrincipalAuth{},
	}, nil)
	if err == nil {
		t.Fatal("authenticated WebSocket without ControllerEndpointID succeeded")
	}
}

func TestVerifyEndpointPrincipalRejectsImpersonation(t *testing.T) {
	p := &principal.Principal{EndpointID: usp.EndpointID("os::trusted-agent")}
	if err := verifyEndpointPrincipal(p, "os::trusted-agent"); err != nil {
		t.Fatalf("matching endpoint rejected: %v", err)
	}
	if err := verifyEndpointPrincipal(p, "os::other-agent"); !errors.Is(err, errEndpointIDMismatch) {
		t.Fatalf("mismatched endpoint error = %v, want errEndpointIDMismatch", err)
	}
}

func TestVerifyMQTTReplyTopicRejectsCrossAgentTopic(t *testing.T) {
	p := &principal.Principal{MQTTTopic: "/usp/agent/trusted"}
	if err := verifyMQTTReplyTopic(p, "/usp/agent/trusted"); err != nil {
		t.Fatalf("matching topic rejected: %v", err)
	}
	if err := verifyMQTTReplyTopic(p, "/usp/agent/other"); !errors.Is(err, errMQTTReplyTopicMismatch) {
		t.Fatalf("cross-agent topic error = %v, want errMQTTReplyTopicMismatch", err)
	}
}

func TestMQTTTopicACLIsPrincipalScoped(t *testing.T) {
	p := &principal.Principal{MQTTTopic: "/usp/agent/trusted"}
	controller := "/usp/controller"

	writeCases := map[string]bool{
		controller: true,
		controller + replyToKey + EscapeReplyTo(p.MQTTTopic):        true,
		controller + replyToKey + EscapeReplyTo("/usp/agent/other"): false,
		"/usp/agent/trusted": false,
		"/other":             false,
	}
	for topic, want := range writeCases {
		if got := mqttTopicAllowed(p, controller, topic, true); got != want {
			t.Errorf("write ACL %q = %v, want %v", topic, got, want)
		}
	}

	readCases := map[string]bool{
		p.MQTTTopic:          true,
		p.MQTTTopic + "/#":   true,
		p.MQTTTopic + "/one": true,
		"/usp/agent/other":   false,
		"/usp/agent/other/#": false,
		p.MQTTTopic + "/+":   false,
	}
	for topic, want := range readCases {
		if got := mqttTopicAllowed(p, controller, topic, false); got != want {
			t.Errorf("read ACL %q = %v, want %v", topic, got, want)
		}
	}
}

func TestAuthenticateCertificateFailsClosed(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte("cert")}
	wantErr := errors.New("denied")
	if _, err := authenticateCertificate(context.Background(), staticPrincipalAuth{err: wantErr}, cert); !errors.Is(err, wantErr) {
		t.Fatalf("auth error = %v, want %v", err, wantErr)
	}
	if _, err := authenticateCertificate(context.Background(), staticPrincipalAuth{}, nil); !errors.Is(err, errClientCertificateRequired) {
		t.Fatalf("nil certificate error = %v, want errClientCertificateRequired", err)
	}
}
