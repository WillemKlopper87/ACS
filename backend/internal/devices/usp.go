package devices

import (
	"context"

	"github.com/google/uuid"

	"acs/internal/cwmp"
)

// UpsertFromOnBoard records (or refreshes) a device from a USP
// OnBoardRequest Notify, landing it on the exact same oui_serial natural
// key (internal/cwmp.DeviceID.NaturalKey) a CWMP Inform for the same
// physical unit would produce — so a dual-stack device is one devices row,
// not two. It follows UpsertFromInform's INSERT ... ON CONFLICT shape, but
// additionally records 'USP' in management_protocols instead of 'CWMP',
// without disturbing whatever protocols that array already holds (a device
// onboarded first via CWMP keeps 'CWMP' and gains 'USP' here).
func (r *Repository) UpsertFromOnBoard(ctx context.Context, oui, productClass, serialNumber string) (*Device, error) {
	id := cwmp.DeviceID{OUI: oui, ProductClass: productClass, SerialNumber: serialNumber}

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number,
		                      online_status, first_seen_at, last_updated_at, management_protocols)
		VALUES ($1, $2, '', $3, $4, $5, 'ONLINE', now(), now(), ARRAY['USP'])
		ON CONFLICT (oui_serial) DO UPDATE SET
			online_status = 'ONLINE',
			last_updated_at = now(),
			management_protocols = CASE WHEN 'USP' = ANY(devices.management_protocols) THEN devices.management_protocols ELSE array_append(devices.management_protocols, 'USP') END
		RETURNING `+deviceColumns, uuid.New().String(), id.NaturalKey(), id.OUI, id.ProductClass, id.SerialNumber)

	return scanDevice(row)
}
