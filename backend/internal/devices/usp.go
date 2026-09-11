package devices

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"acs/internal/cwmp"
)

// ErrEmptyIdentity is returned when a USP identity upsert is attempted with
// an empty OUI or SerialNumber. This mirrors the CWMP Inform path's guard
// (cmd/acs/session.go's handleInform: "OUI and SerialNumber are required
// for a device identity") -- ProductClass may legitimately be empty, same
// as CWMP tolerates, but OUI and SerialNumber compose the natural key
// every device row is matched on. Without this guard, every
// empty-identity OnBoardRequest/GetResp collapses onto the same
// oui_serial = "+..." row, and usp_agents.LinkUspAgent's
// ON CONFLICT (device_id) DO UPDATE then silently steals that row's
// endpoint binding from whichever agent linked it last.
var ErrEmptyIdentity = errors.New("usp: OUI and SerialNumber are required for a device identity")

// UpsertFromOnBoard records (or refreshes) a device from a USP
// OnBoardRequest Notify, landing it on the exact same oui_serial natural
// key (internal/cwmp.DeviceID.NaturalKey) a CWMP Inform for the same
// physical unit would produce — so a dual-stack device is one devices row,
// not two. It follows UpsertFromInform's INSERT ... ON CONFLICT shape, but
// additionally records 'USP' in management_protocols instead of 'CWMP',
// without disturbing whatever protocols that array already holds (a device
// onboarded first via CWMP keeps 'CWMP' and gains 'USP' here).
//
// data_model_root is set to DEVICE2 only on the INSERT path (design spec
// §5.3: "data_model_root is always DEVICE2 for USP agents") -- the ON
// CONFLICT UPDATE deliberately leaves it alone, since a CWMP-discovered
// root for the same physical device (which might genuinely be IGD1) must
// not be overwritten by a later USP onboarding of that device.
func (r *Repository) UpsertFromOnBoard(ctx context.Context, oui, productClass, serialNumber string) (*Device, error) {
	if oui == "" || serialNumber == "" {
		return nil, ErrEmptyIdentity
	}
	id := cwmp.DeviceID{OUI: oui, ProductClass: productClass, SerialNumber: serialNumber}

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number,
		                      data_model_root, online_status, first_seen_at, last_updated_at, management_protocols)
		VALUES ($1, $2, '', $3, $4, $5, 'DEVICE2', 'ONLINE', now(), now(), ARRAY['USP'])
		ON CONFLICT (oui_serial) DO UPDATE SET
			online_status = 'ONLINE',
			last_updated_at = now(),
			management_protocols = CASE WHEN 'USP' = ANY(devices.management_protocols) THEN devices.management_protocols ELSE array_append(devices.management_protocols, 'USP') END
		RETURNING `+deviceColumns, uuid.New().String(), id.NaturalKey(), id.OUI, id.ProductClass, id.SerialNumber)

	return scanDevice(row)
}
