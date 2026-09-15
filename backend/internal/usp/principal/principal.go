// Package principal defines the authenticated identity a USP transport may
// trust. It intentionally contains no persistence or device-domain logic: the
// internal/usp tree is protocol-only, and cmd/uspc injects an implementation
// of CertificateAuthenticator from outside that tree.
package principal

import (
	"context"
	"crypto/x509"
	"errors"
	"time"

	"acs/internal/usp"
)

// ErrNotFound means a verified certificate or EndpointID is not bound to an
// enabled USP principal. Callers must fail closed on this error.
var ErrNotFound = errors.New("usp transport principal not found")

// Principal is the authenticated identity a transport is allowed to trust.
// MQTTTopic is the exact agent response-topic root; no wildcards are stored.
type Principal struct {
	DeviceID         string
	EndpointID       usp.EndpointID
	ClientCertSHA256 string
	MQTTTopic        string
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// CertificateAuthenticator is the narrow dependency transports need. The
// certificate passed here has already been chain-verified by crypto/tls; an
// implementation resolves that cryptographic identity to the application
// identity the connection may use.
type CertificateAuthenticator interface {
	AuthenticateCertificate(ctx context.Context, cert *x509.Certificate) (*Principal, error)
}
