// Mesh satellite -> gateway linking (field test 2026-10-30, migration 0083).
//
// A mesh satellite such as the Nokia WiFi Beacon 3.1 is managed over its own
// CWMP session but lives entirely behind a gateway (here a FastMile 5G
// Gateway 2): its Connection Request URL is on the gateway LAN, its Wi-Fi
// clients are reported by the gateway's EasyMesh controller, and after a
// reboot it only comes back once the mesh backhaul rejoins. TR-111 gives the
// satellite a standard way to say which gateway that is:
// Device.GatewayInfo.{ManufacturerOUI,ProductClass,SerialNumber}. Those three
// values are exactly the gateway's own CWMP DeviceId, so the link is resolved
// against the devices.oui_serial natural key.
package devices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"acs/internal/cwmp"
	"acs/internal/store"
)

// TR-111 GatewayInfo parameter paths (TR-181 Device:2).
const (
	GatewayInfoPrefix          = "Device.GatewayInfo."
	GatewayInfoManufacturerOUI = "Device.GatewayInfo.ManufacturerOUI"
	GatewayInfoProductClass    = "Device.GatewayInfo.ProductClass"
	GatewayInfoSerialNumber    = "Device.GatewayInfo.SerialNumber"
)

// GatewayInfoPaths are the leaves read to resolve a satellite's gateway.
var GatewayInfoPaths = []string{GatewayInfoManufacturerOUI, GatewayInfoProductClass, GatewayInfoSerialNumber}

// GatewayInfoReadPaths returns the GatewayInfo leaves present in a
// discovered name tree (nil when the device has no TR-111 GatewayInfo, which
// is the normal case for a gateway itself). Only SerialNumber is required:
// ProductClass is optional in the natural key, and an absent OUI just means
// the satellite cannot name its gateway.
func GatewayInfoReadPaths(names map[string]bool) []string {
	if _, ok := names[GatewayInfoSerialNumber]; !ok {
		return nil
	}
	out := make([]string, 0, len(GatewayInfoPaths))
	for _, path := range GatewayInfoPaths {
		if _, ok := names[path]; ok {
			out = append(out, path)
		}
	}
	return out
}

// GatewayIdentityFromValues builds the gateway's CWMP identity from cached
// GatewayInfo values. ok is false when the satellite reports no usable
// gateway (blank OUI or serial), which callers treat as "no gateway".
func GatewayIdentityFromValues(values map[string]string) (id cwmp.DeviceID, ok bool) {
	id = cwmp.DeviceID{
		OUI:          strings.TrimSpace(values[GatewayInfoManufacturerOUI]),
		ProductClass: strings.TrimSpace(values[GatewayInfoProductClass]),
		SerialNumber: strings.TrimSpace(values[GatewayInfoSerialNumber]),
	}.Normalized()
	if id.OUI == "" || id.SerialNumber == "" {
		return cwmp.DeviceID{}, false
	}
	return id, true
}

// GatewayLink is the outcome of LinkGateway.
type GatewayLink struct {
	// GatewayOUISerial is the reported gateway natural key ("" when cleared).
	GatewayOUISerial string
	// GatewayDeviceID is the resolved gateway row, nil when the reported
	// gateway is not (yet) registered.
	GatewayDeviceID *string
	// Changed reports whether the stored link differs from before.
	Changed bool
}

// LinkGateway records the gateway a device reports via TR-111 GatewayInfo
// and resolves it to a registered device. Resolution is by the exact
// oui_serial natural key first; when that misses (e.g. GatewayInfo's
// ProductClass differs from the one the gateway informs with), a unique
// OUI + SerialNumber match is accepted. A device never links to itself.
// Pass ok=false (no usable GatewayInfo) to clear the link.
func (r *Repository) LinkGateway(ctx context.Context, deviceID string, gateway cwmp.DeviceID, ok bool) (GatewayLink, error) {
	var link GatewayLink
	var key, resolved any
	if ok {
		link.GatewayOUISerial = gateway.NaturalKey()
		key = link.GatewayOUISerial
		id, err := r.resolveGateway(ctx, deviceID, gateway)
		if err != nil {
			return GatewayLink{}, err
		}
		if id != "" {
			link.GatewayDeviceID = &id
			resolved = id
		}
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE devices SET gateway_oui_serial = $2, gateway_device_id = $3, last_updated_at = now()
		WHERE id = $1
		  AND (gateway_oui_serial IS DISTINCT FROM $2::text OR gateway_device_id IS DISTINCT FROM $3::uuid)`,
		deviceID, key, resolved)
	if err != nil {
		return GatewayLink{}, fmt.Errorf("update device gateway link: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return GatewayLink{}, fmt.Errorf("update device gateway link: %w", err)
	}
	link.Changed = n > 0
	return link, nil
}

func (r *Repository) resolveGateway(ctx context.Context, deviceID string, gateway cwmp.DeviceID) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx,
		`SELECT id::text FROM devices WHERE oui_serial = $1 AND id <> $2`, gateway.NaturalKey(), deviceID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("resolve gateway by natural key: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id::text FROM devices
		WHERE serial_number = $1
		  AND upper(regexp_replace(COALESCE(oui, ''), '[^0-9A-Za-z]', '', 'g')) = $2
		  AND id <> $3
		LIMIT 2`, gateway.SerialNumber, gateway.OUI, deviceID)
	if err != nil {
		return "", fmt.Errorf("resolve gateway by oui and serial: %w", err)
	}
	defer rows.Close()
	var matches []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return "", fmt.Errorf("scan gateway candidate: %w", err)
		}
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate gateway candidates: %w", err)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return "", nil // unknown or ambiguous: keep the reported key, no FK
}

// AdoptSatellites links devices that reported this gateway's natural key
// before the gateway itself was registered (or after it was deleted and
// re-created). Returns the number of satellites linked.
func (r *Repository) AdoptSatellites(ctx context.Context, gateway *Device) (int, error) {
	if gateway == nil {
		return 0, nil
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE devices SET gateway_device_id = $1, last_updated_at = now()
		WHERE gateway_oui_serial = $2 AND gateway_device_id IS NULL AND id <> $1`,
		gateway.ID, gateway.OUISerial)
	if err != nil {
		return 0, fmt.Errorf("adopt mesh satellites: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("adopt mesh satellites: %w", err)
	}
	return int(n), nil
}

// ListSatellites returns the devices linked to gatewayID, honouring the
// same tenancy scope shape as List (scoped=false means unrestricted).
func (r *Repository) ListSatellites(ctx context.Context, gatewayID string, customerIDs []string, scoped bool) ([]Device, error) {
	query := `SELECT ` + deviceColumns + ` FROM devices WHERE gateway_device_id = $1`
	args := []any{gatewayID}
	if scoped {
		query += ` AND customer_id::text = ANY($2)`
		args = append(args, store.StringArray(customerIDs))
	}
	query += ` ORDER BY last_inform_at DESC NULLS LAST, oui_serial`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list mesh satellites: %w", err)
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mesh satellites: %w", err)
	}
	return out, nil
}

// MeshRelations is the satellite/gateway adjacency for a page of devices.
type MeshRelations struct {
	// Gateway maps a satellite id to its gateway id.
	Gateway map[string]string
	// Satellites maps a gateway id to its satellite ids (sorted).
	Satellites map[string][]string
}

// MeshRelationsFor loads both directions of the mesh link for deviceIDs in
// two queries. The counterpart device must be visible in the caller's scope
// (scoped=false: unrestricted), so a relationship never names a device the
// caller could not read.
func (r *Repository) MeshRelationsFor(ctx context.Context, deviceIDs []string, customerIDs []string, scoped bool) (MeshRelations, error) {
	out := MeshRelations{Gateway: map[string]string{}, Satellites: map[string][]string{}}
	if len(deviceIDs) == 0 {
		return out, nil
	}
	scope := ""
	args := []any{store.StringArray(deviceIDs)}
	if scoped {
		scope = ` AND counterpart.customer_id::text = ANY($2)`
		args = append(args, store.StringArray(customerIDs))
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT d.id::text, counterpart.id::text FROM devices d
		JOIN devices counterpart ON counterpart.id = d.gateway_device_id
		WHERE d.id::text = ANY($1)`+scope, args...)
	if err != nil {
		return MeshRelations{}, fmt.Errorf("query mesh gateways: %w", err)
	}
	for rows.Next() {
		var sat, gw string
		if err := rows.Scan(&sat, &gw); err != nil {
			rows.Close()
			return MeshRelations{}, fmt.Errorf("scan mesh gateway: %w", err)
		}
		out.Gateway[sat] = gw
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return MeshRelations{}, fmt.Errorf("iterate mesh gateways: %w", err)
	}
	rows.Close()

	rows, err = r.db.QueryContext(ctx, `
		SELECT counterpart.gateway_device_id::text, counterpart.id::text FROM devices counterpart
		WHERE counterpart.gateway_device_id::text = ANY($1)`+scope+`
		ORDER BY counterpart.id`, args...)
	if err != nil {
		return MeshRelations{}, fmt.Errorf("query mesh satellites: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var gw, sat string
		if err := rows.Scan(&gw, &sat); err != nil {
			return MeshRelations{}, fmt.Errorf("scan mesh satellite: %w", err)
		}
		out.Satellites[gw] = append(out.Satellites[gw], sat)
	}
	if err := rows.Err(); err != nil {
		return MeshRelations{}, fmt.Errorf("iterate mesh satellites: %w", err)
	}
	return out, nil
}
