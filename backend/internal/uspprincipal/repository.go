// Package uspprincipal persists the application-level trust binding used by
// cmd/uspc before a USP transport connection may claim a device identity.
//
// The protocol-facing Principal type and authentication interface deliberately
// live under internal/usp/principal; this package is outside internal/usp so
// persistence can depend on database state without violating the enforced
// protocol/domain boundary.
package uspprincipal

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"acs/internal/usp"
	"acs/internal/usp/principal"
)

var ErrInvalidBinding = errors.New("invalid usp transport principal binding")

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// CertificateSHA256 returns the lowercase SHA-256 fingerprint of a leaf
// certificate's DER bytes. Raw DER, rather than Subject/CommonName, is used so
// certificates with the same human-readable subject remain distinct principals.
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
// the primary key, the old certificate fingerprint stops authenticating when
// this transaction commits.
func (r *Repository) BindCertificate(ctx context.Context, deviceID string, endpointID usp.EndpointID, cert *x509.Certificate, mqttTopic string) (*principal.Principal, error) {
	deviceID = strings.TrimSpace(deviceID)
	endpoint := strings.TrimSpace(string(endpointID))
	mqttTopic = strings.TrimSpace(mqttTopic)
	fingerprint := CertificateSHA256(cert)
	if r == nil || r.db == nil || deviceID == "" || endpoint == "" || fingerprint == "" || mqttTopic == "" || strings.ContainsAny(mqttTopic, "#+") {
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
// indistinguishable to the transport and fail closed as principal.ErrNotFound.
func (r *Repository) AuthenticateCertificate(ctx context.Context, cert *x509.Certificate) (*principal.Principal, error) {
	fingerprint := CertificateSHA256(cert)
	if fingerprint == "" {
		return nil, principal.ErrNotFound
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT `+principalColumns+`
		FROM usp_transport_principals
		WHERE client_cert_sha256 = $1 AND enabled`, fingerprint)
	p, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, principal.ErrNotFound
	}
	return p, err
}

// ByEndpointID resolves the enabled principal currently allowed to speak as
// endpointID. cmd/uspc uses this as an application-layer identity check after
// the transport has authenticated the certificate.
func (r *Repository) ByEndpointID(ctx context.Context, endpointID usp.EndpointID) (*principal.Principal, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+principalColumns+`
		FROM usp_transport_principals
		WHERE endpoint_id = $1 AND enabled`, string(endpointID))
	p, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, principal.ErrNotFound
	}
	return p, err
}

// Disable revokes a device's USP transport principal without deleting its
// audit-relevant binding metadata.
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
		return principal.ErrNotFound
	}
	return nil
}

// ByDeviceID returns binding metadata for operator-facing management. It never
// contains private-key material; only the public certificate fingerprint is
// stored in this table.
func (r *Repository) ByDeviceID(ctx context.Context, deviceID string) (*principal.Principal, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+principalColumns+`
		FROM usp_transport_principals
		WHERE device_id = $1`, deviceID)
	p, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, principal.ErrNotFound
	}
	return p, err
}

func scanPrincipal(s interface{ Scan(...any) error }) (*principal.Principal, error) {
	var p principal.Principal
	var endpoint string
	if err := s.Scan(&p.DeviceID, &endpoint, &p.ClientCertSHA256, &p.MQTTTopic, &p.Enabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.EndpointID = usp.EndpointID(endpoint)
	return &p, nil
}
