package devices

import (
	"context"

	"github.com/google/uuid"

	"acs/internal/cwmp"
)

// EnsureBootstrapRegistration returns the concrete ACS device row that a
// constrained CWMP bootstrap exchange may graduate. A first-contact device is
// created OFFLINE with no Inform/authentication state and no tenant assignment.
// If an operator pre-registered the same natural identity first, all ownership,
// tags and other operator-managed fields are preserved.
//
// The only enrichment allowed on an existing row is filling a previously
// UNKNOWN data-model root from the device's own first-contact Inform. This is
// needed solely to choose the correct ManagementServer.Username/Password paths
// for the graduation RPC; it does not mark the root as discovery-confirmed.
func (r *Repository) EnsureBootstrapRegistration(ctx context.Context, id cwmp.DeviceID, inferredRoot string) (*Device, error) {
	id = id.Normalized()
	root := DataModelRootUnknown
	switch inferredRoot {
	case DataModelRootDevice2, DataModelRootIGD1:
		root = inferredRoot
	}

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number,
		                      data_model_root, online_status, first_seen_at, last_updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'OFFLINE', now(), now())
		ON CONFLICT (oui_serial) DO UPDATE SET
			manufacturer = CASE
				WHEN COALESCE(devices.manufacturer, '') = '' THEN EXCLUDED.manufacturer
				ELSE devices.manufacturer
			END,
			data_model_root = CASE
				WHEN devices.data_model_root = 'UNKNOWN' AND EXCLUDED.data_model_root <> 'UNKNOWN'
					THEN EXCLUDED.data_model_root
				ELSE devices.data_model_root
			END,
			last_updated_at = now()
		RETURNING `+deviceColumns,
		uuid.New().String(), id.NaturalKey(), id.Manufacturer, id.OUI,
		id.ProductClass, id.SerialNumber, root)

	return scanDevice(row)
}
