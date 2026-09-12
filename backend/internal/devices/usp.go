package devices

import (
	"context"
	"database/sql"
	"errors"

	"acs/internal/cwmp"
)

// ErrEmptyIdentity is returned when a USP identity reconcile is attempted
// with an empty OUI or SerialNumber. This mirrors the CWMP Inform path's
// guard (cmd/acs/session.go's handleInform: "OUI and SerialNumber are
// required for a device identity") -- ProductClass may legitimately be
// empty, same as CWMP tolerates, but OUI and SerialNumber compose the
// natural key every device row is matched on. Without this guard, every
// empty-identity OnBoardRequest/GetResp collapses onto the same
// oui_serial = "+..." row, and usp_agents.LinkUspAgent's
// ON CONFLICT (device_id) DO UPDATE then silently steals that row's
// endpoint binding from whichever agent linked it last.
var ErrEmptyIdentity = errors.New("usp: OUI and SerialNumber are required for a device identity")

// ErrUnknownDevice is returned by ReconcileFromOnBoard when no devices
// row exists for the given identity -- the identity-level half of the USP
// agent allowlist (design
// docs/superpowers/specs/2026-09-12-usp-agent-allowlist-design.md S2.2).
// A device becomes "known" via PreRegister (bulk import), a prior CWMP
// Inform, or a prior USP onboarding -- never via this function itself,
// which no longer creates rows.
var ErrUnknownDevice = errors.New("usp: no devices row exists for this identity -- pre-register the device before its first USP contact")

// ReconcileFromOnBoard records (or refreshes) a device from a USP
// OnBoardRequest Notify, landing it on the exact same oui_serial natural
// key (internal/cwmp.DeviceID.NaturalKey) a CWMP Inform for the same
// physical unit would produce — so a dual-stack device is one devices row,
// not two.
//
// Unlike its predecessor UpsertFromOnBoard, this never creates a devices
// row: an identity with no existing row returns ErrUnknownDevice. This is
// the identity-level gate of the USP agent allowlist -- a device becomes
// "known" via PreRegister, a prior CWMP Inform, or a prior USP onboarding
// (before this gate existed), never via this function.
//
// It additionally records 'USP' in management_protocols instead of
// 'CWMP', without disturbing whatever protocols that array already holds
// (a device onboarded first via CWMP keeps 'CWMP' and gains 'USP' here),
// and fills data_model_root in to DEVICE2 only when it is still at the
// devices table's own 'UNKNOWN' default (design spec §5.3: "data_model_root
// is always DEVICE2 for USP agents") -- a real CWMP-discovered root
// ('IGD1', or a CWMP-set 'DEVICE2') is never overwritten, only the
// genuinely-never-set case (e.g. a device that reached this row only via
// PreRegister, with no CWMP contact ever, then onboards via USP for the
// first time) is filled in.
func (r *Repository) ReconcileFromOnBoard(ctx context.Context, oui, productClass, serialNumber string) (*Device, error) {
	if oui == "" || serialNumber == "" {
		return nil, ErrEmptyIdentity
	}
	id := cwmp.DeviceID{OUI: oui, ProductClass: productClass, SerialNumber: serialNumber}

	row := r.db.QueryRowContext(ctx, `
		UPDATE devices SET
			online_status = 'ONLINE',
			last_updated_at = now(),
			management_protocols = CASE WHEN 'USP' = ANY(management_protocols) THEN management_protocols ELSE array_append(management_protocols, 'USP') END,
			data_model_root = CASE WHEN data_model_root = 'UNKNOWN' THEN 'DEVICE2' ELSE data_model_root END
		WHERE oui_serial = $1
		RETURNING `+deviceColumns, id.NaturalKey())

	device, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnknownDevice
	}
	if err != nil {
		return nil, err
	}
	return device, nil
}
