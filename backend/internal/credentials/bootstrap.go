package credentials

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// EnsurePendingCWMPDigest returns the one pending CPE->ACS Digest credential
// for a device, creating it when necessary. The device row is locked for the
// transaction so concurrent bootstrap Informs cannot create multiple live
// pending identities for the same device.
//
// The credential remains PENDING until cmd/acs receives a later Inform that
// authenticates with it and whose OUI/ProductClass/Serial resolves to this same
// DeviceID. Merely proving the HTTP Digest secret is intentionally insufficient
// to activate it.
func (r *Repository) EnsurePendingCWMPDigest(ctx context.Context, deviceID string) (*Credential, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin ensure pending CWMP credential tx: %w", err)
	}
	defer tx.Rollback()

	// Serialize bootstrap graduation per device using the row the credential's
	// foreign key already depends on. This is narrower than a table lock and
	// also proves the target device still exists before creating a secret.
	var lockedDeviceID string
	if err := tx.QueryRowContext(ctx, `SELECT id::text FROM devices WHERE id = $1 FOR UPDATE`, deviceID).Scan(&lockedDeviceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("lock bootstrap device: %w", err)
	}

	row := tx.QueryRowContext(ctx, `
		SELECT `+credColumns+` FROM device_credentials
		WHERE device_id = $1 AND credential_type = $2 AND status = $3
		ORDER BY version DESC LIMIT 1`, deviceID, TypeCWMPDigest, StatusPending)
	cred, err := r.scanCredential(row)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit existing pending CWMP credential tx: %w", err)
		}
		return cred, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	username, password, err := GenerateUsernamePassword()
	if err != nil {
		return nil, err
	}
	encryptedPassword, err := r.encrypt(password)
	if err != nil {
		return nil, err
	}

	var nextVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1 FROM device_credentials
		WHERE device_id = $1 AND credential_type = $2`, deviceID, TypeCWMPDigest).Scan(&nextVersion); err != nil {
		return nil, fmt.Errorf("compute next CWMP Digest credential version: %w", err)
	}

	id := uuid.New().String()
	commandKey := "cwmp-bootstrap-" + id
	row = tx.QueryRowContext(ctx, `
		INSERT INTO device_credentials (id, device_id, credential_type, version, username, password, command_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+credColumns,
		id, deviceID, TypeCWMPDigest, nextVersion, username, encryptedPassword, commandKey)
	cred, err = r.scanCredential(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit pending CWMP credential tx: %w", err)
	}
	return cred, nil
}
