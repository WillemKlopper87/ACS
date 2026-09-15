// Package principal owns the durable trust binding used before a USP
// transport connection is allowed to claim an EndpointID.
//
// usp_agents is intentionally live connection state, so it cannot be the
// authority for authenticating the same connection that populates it. A
// Principal is pre-provisioned against a devices row and binds a verified
// client-certificate fingerprint to one USP EndpointID and one MQTT response
// topic. WebSocket and MQTT transports consume this package through a narrow
// certificate-authentication interface; they never query Postgres directly.
package principal

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"acs/internal/usp"
)

var (
	// ErrNotFound means the verified certificate is not bound to an enabled
	// USP principal. Callers must fail closed on this error.
	ErrNotFound = errors.New("usp transport principal not found")
	// ErrInvalidBinding means an operator attempted to provision a binding
	// that cannot safely be used for transport authorization.
	ErrInvalidBinding = errors.New("invalid usp transport principal binding")
)

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

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// CertificateSHA256 returns the lowercase SHA-256 fingerprint of a leaf
// certificate's DER bytes. Raw, not Subject/CommonName, is used so two
// certificates with the same human-readable subject are still distinct
// principals.
func CertificateSHA256(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

const principalColumns = `device_id, endpoint_id, client_cert_sha256, mqtt_topic, enabled, created_at, updated_at`

// BindCertificate creates or replaces the one transport principal for a
// device. Re-binding is an explicit credential rotation: because device_id is
// the primary key, the old certificate fingerprint stops authenticating as
// soon as this transaction commits.
func (r *Repository) BindCertificate(ctx context.Context, deviceID string, endpointID usp.EndpointID, cert *x509.Certificate, mqttTopic string) (*Principal, error) {
	deviceID = strings.TrimSpace(deviceID)
	endpoint := strings.TrimSpace(string(endpointID))
	mqttTopic = strings.TrimSpace(mqttTopic)
	fingerprint := CertificateSHA256(cert)
	if deviceID == "" || endpoint == "" || fingerprint == "" || mqttTopic == "" || strings.ContainsAny(mqttTopic, "#+") {
		return nil, ErrInvalidBinding
	}

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO usp_transport_principals
			(device_id, endpoint_id, client_cert_sha256, mqtt_topic, enabled, updated_at)
		VALUES ($1, $2, $3, $4, true, now())
		ON CONFLICT (device_id) DO UPDATE SET
			endpoint_id = EXCLUDED.endpoint_id,
			client_cert_sha256 = EXCLUDED.client_cert_sha256,
			mqtt_topic = EXCLUDED.mqtt_topic,
			enabled = true,
			updated_at = now()
		RETURNING `+principalColumns,
		deviceID, endpoint, fingerprint, mqttTopic)
	return scanPrincipal(row)
}

// AuthenticateCertificate resolves an already TLS-verified leaf certificate
// to an enabled principal. Disabled, unknown, or nil certificates are all
// indistinguishable to the transport and fail closed as ErrNotFound.
func (r *Repository) AuthenticateCertificate(ctx context.Context, cert *x509.Certificate) (*Principal, error) {
	fingerprint := CertificateSHA256(cert)
	if fingerprint == "" {
		return nil, ErrNotFound
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT `+principalColumns+`
		FROM usp_transport_principals
		WHERE client_cert_sha256 = $1 AND enabled`, fingerprint)
	p, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ByEndpointID resolves the enabled principal currently allowed to speak as
// endpointID. cmd/uspc uses this as an independent application-layer check:
// after the transport has authenticated the client certificate, an
// OnBoardRequest/probe identity must still resolve to this principal's bound
// devices row before reconciliation is allowed to mutate live device state.
func (r *Repository) ByEndpointID(ctx context.Context, endpointID usp.EndpointID) (*Principal, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+principalColumns+`
		FROM usp_transport_principals
		WHERE endpoint_id = $1 AND enabled`, string(endpointID))
	p, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// Disable revokes a device's USP transport principal without deleting its
// audit-relevant binding metadata. A disabled certificate can no longer open
// either MQTT or WebSocket sessions.
func (r *Repository) Disable(ctx context.Context, deviceID string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE usp_transport_principals
		SET enabled = false, updated_at = now()
		WHERE device_id = $1`, deviceID)
	if err != nil {
		return fmt.Errorf("disable usp transport principal: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("disable usp transport principal: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ByDeviceID returns the binding metadata used by the operator API. It never
// contains private-key material: only the public certificate fingerprint is
// stored in this table.
func (r *Repository) ByDeviceID(ctx context.Context, deviceID string) (*Principal, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+principalColumns+`
		FROM usp_transport_principals
		WHERE device_id = $1`, deviceID)
	p, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func scanPrincipal(s interface{ Scan(...any) error }) (*Principal, error) {
	var p Principal
	var endpoint string
	if err := s.Scan(&p.DeviceID, &endpoint, &p.ClientCertSHA256, &p.MQTTTopic, &p.Enabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.EndpointID = usp.EndpointID(endpoint)
	return &p, nil
}
