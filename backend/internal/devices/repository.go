package devices

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"acs/internal/cwmp"
	"acs/internal/store"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const deviceColumns = `id, oui_serial, manufacturer, oui, product_class, serial_number,
	data_model_root, online_status, last_inform_at, last_inform_event_codes,
	connection_request_url, connection_request_mode, last_connection_request_at,
	last_connection_request_status, last_inform_after_connection_request_at,
	first_seen_at, last_updated_at, tags, cwmp_auth_mode, data_model_root_confirmed_at,
	udp_connection_request_address, nat_detected, customer_id, location`

// UpsertFromInform records (or refreshes) a device from an Inform message.
// data_model_root is left untouched (defaulting to UNKNOWN for a new
// device) — Phase 1's production path has no GetParameterNames probe;
// root detection is populated starting Phase 2 (build plan §4 Phase 2).
func (r *Repository) UpsertFromInform(ctx context.Context, id cwmp.DeviceID, eventCodes []string) (*Device, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number,
		                      online_status, last_inform_at, last_inform_event_codes,
		                      first_seen_at, last_updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'ONLINE', now(), $7, now(), now())
		ON CONFLICT (oui_serial) DO UPDATE SET
			manufacturer = EXCLUDED.manufacturer,
			oui = EXCLUDED.oui,
			product_class = EXCLUDED.product_class,
			serial_number = EXCLUDED.serial_number,
			online_status = 'ONLINE',
			last_inform_at = now(),
			last_inform_event_codes = EXCLUDED.last_inform_event_codes,
			last_updated_at = now()
		RETURNING `+deviceColumns, uuid.New().String(), id.NaturalKey(), id.Manufacturer, id.OUI,
		id.ProductClass, id.SerialNumber, store.StringArray(eventCodes))

	return scanDevice(row)
}

// PreRegister creates (or updates the customer/tags of) a device row ahead
// of its first real Inform — the bulk-import backing primitive
// (admin-platform backlog). Keyed on the same oui_serial natural key
// UpsertFromInform uses, so once the real device Informs, that ON
// CONFLICT match enriches this same row with everything only the CPE
// itself can report (data model root, parameter cache, etc.) rather than
// creating a duplicate.
func (r *Repository) PreRegister(ctx context.Context, ouiSerial, manufacturer, oui, productClass, serialNumber string, customerID *string, tags []string) (*Device, error) {
	if tags == nil {
		tags = []string{}
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO devices (id, oui_serial, manufacturer, oui, product_class, serial_number,
		                      online_status, first_seen_at, last_updated_at, customer_id, tags)
		VALUES ($1, $2, $3, $4, $5, $6, 'OFFLINE', now(), now(), $7, $8)
		ON CONFLICT (oui_serial) DO UPDATE SET
			customer_id = EXCLUDED.customer_id,
			tags = EXCLUDED.tags,
			last_updated_at = now()
		RETURNING `+deviceColumns,
		uuid.New().String(), ouiSerial, manufacturer, oui, productClass, serialNumber, customerID, store.StringArray(tags))
	return scanDevice(row)
}

func (r *Repository) Get(ctx context.Context, id string) (*Device, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE id = $1`, id)
	return scanDevice(row)
}

// GetByOUIserial fetches a device row by the natural identifier used in the
// Inform handshake (OUI + ProductClass + SerialNumber, or OUI + SerialNumber when
// ProductClass is absent).
func (r *Repository) GetByOUIserial(ctx context.Context, ouiSerial string) (*Device, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE oui_serial = $1`, ouiSerial)
	return scanDevice(row)
}

// RefreshLiveness re-classifies stale devices by the last Inform time. A fresh
// device stays ONLINE, a quiet-but-healthy device drops to OFFLINE, and a long-
// stale device is marked UNREACHABLE.
func (r *Repository) RefreshLiveness(ctx context.Context, onlineThreshold, unreachableThreshold time.Duration) (offlineCount, unreachableCount int, err error) {
	if onlineThreshold <= 0 {
		onlineThreshold = 5 * time.Minute
	}
	if unreachableThreshold <= 0 {
		unreachableThreshold = 90 * time.Minute
	}

	_, err = r.db.ExecContext(ctx, `
		UPDATE devices
		SET online_status = CASE
			WHEN last_inform_at IS NULL THEN 'OFFLINE'
			WHEN last_inform_at > now() - $1::interval THEN 'ONLINE'
			WHEN last_inform_at > now() - $2::interval THEN 'OFFLINE'
			ELSE 'UNREACHABLE'
		END,
		last_updated_at = now()
		WHERE last_inform_at IS NULL OR last_inform_at <= now() - $1::interval OR last_inform_at <= now() - $2::interval
	`, formatInterval(onlineThreshold), formatInterval(unreachableThreshold))
	if err != nil {
		return 0, 0, fmt.Errorf("refresh liveness: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT online_status, COUNT(*)
		FROM devices
		WHERE online_status IN ('OFFLINE', 'UNREACHABLE')
		GROUP BY online_status
	`)
	if err != nil {
		return 0, 0, fmt.Errorf("count liveness states: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return 0, 0, fmt.Errorf("scan liveness state: %w", err)
		}
		switch status {
		case "OFFLINE":
			offlineCount = count
		case "UNREACHABLE":
			unreachableCount = count
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("finish liveness scan: %w", err)
	}
	return offlineCount, unreachableCount, nil
}

func formatInterval(d time.Duration) string {
	return d.String()
}

// maxPageSize caps page_size (design doc v3 §8.1's ?page/&page_size query
// params) so a client can't request the whole fleet in one response —
// exactly the gap that made the original unbounded List() a scaling
// problem at fleet size (build plan §3.2 called for virtualized frontend
// rendering, but that's moot if the backend ships every row regardless).
const maxPageSize = 500

// ListParams is List's pagination input. Page is 1-based; a zero value
// for either field gets a sane default rather than erroring.
//
// CustomerIDs implements multi-tenancy scoping (admin-platform backlog):
// nil means unrestricted (the caller's operator has no scope rows, or
// auth is disabled), non-nil (even an empty slice) restricts the result
// to devices owned by one of those customers — mirrors
// tenancy.Repository.AccessibleCustomerIDs' (ids, scoped) return shape
// deliberately, so a caller just passes that straight through.
type ListParams struct {
	Page        int
	PageSize    int
	CustomerIDs []string
	Scoped      bool
}

// ListResult carries the page plus the total row count, so a caller can
// compute page count without a second query.
type ListResult struct {
	Items []Device
	Total int
}

func (r *Repository) List(ctx context.Context, params ListParams) (*ListResult, error) {
	page := params.Page
	if page < 1 {
		page = 1
	}
	pageSize := params.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}

	scopeClause := ""
	scopeArgs := []any{}
	if params.Scoped {
		// customer_id is compared as text rather than cast to uuid[] — a
		// caller can legitimately pass an empty CustomerIDs (scoped=true,
		// zero accessible customers), and casting an empty array literal
		// to uuid[] is more fragile across driver versions than just
		// comparing as text.
		scopeClause = "WHERE customer_id::text = ANY($1)"
		scopeArgs = append(scopeArgs, store.StringArray(params.CustomerIDs))
	}

	var total int
	countQuery := `SELECT COUNT(*) FROM devices ` + scopeClause
	if err := r.db.QueryRowContext(ctx, countQuery, scopeArgs...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count devices: %w", err)
	}

	listArgs := append(append([]any{}, scopeArgs...), pageSize, (page-1)*pageSize)
	limitOffset := fmt.Sprintf("LIMIT $%d OFFSET $%d", len(scopeArgs)+1, len(scopeArgs)+2)
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+deviceColumns+` FROM devices `+scopeClause+`
		ORDER BY last_inform_at DESC NULLS LAST
		`+limitOffset,
		listArgs...)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &ListResult{Items: out, Total: total}, nil
}

// UpdateAuthMode records how a device's most recent CWMP request actually
// authenticated (build plan §4 Phase 6 / design doc v3 §11.2).
func (r *Repository) UpdateAuthMode(ctx context.Context, deviceID, mode string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE devices SET cwmp_auth_mode = $2 WHERE id = $1`, deviceID, mode)
	if err != nil {
		return fmt.Errorf("update device auth mode: %w", err)
	}
	return nil
}

// UpdateTags replaces a device's full tag set (build plan §4 Phase 7).
// Freeform labels, not a curated membership like device_groups — a
// simple replace-the-array write is the right shape, no join table.
func (r *Repository) UpdateTags(ctx context.Context, deviceID string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}
	_, err := r.db.ExecContext(ctx, `UPDATE devices SET tags = $2, last_updated_at = now() WHERE id = $1`,
		deviceID, store.StringArray(tags))
	if err != nil {
		return fmt.Errorf("update device tags: %w", err)
	}
	return nil
}

// UpdateLocation sets a device's free-text location (admin-platform
// backlog: Excel reporting's "location" column) — operator-entered
// metadata, same shape as UpdateTags since TR-069 has no standard
// location parameter.
func (r *Repository) UpdateLocation(ctx context.Context, deviceID, location string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE devices SET location = $2, last_updated_at = now() WHERE id = $1`,
		deviceID, nullIfEmpty(location))
	if err != nil {
		return fmt.Errorf("update device location: %w", err)
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows, letting List and
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Get/UpsertFromInform share one scan implementation.
type scanner interface {
	Scan(dest ...any) error
}

func scanDevice(s scanner) (*Device, error) {
	var d Device
	var lastInformAt sql.NullTime
	var eventCodes store.StringArray
	var connectionRequestURL sql.NullString
	var lastConnectionRequestAt sql.NullTime
	var lastConnectionRequestStatus sql.NullString
	var lastInformAfterCR sql.NullTime
	var tags store.StringArray
	var dataModelRootConfirmedAt sql.NullTime
	var udpConnectionRequestAddress sql.NullString
	var natDetected sql.NullBool
	var customerID sql.NullString
	var location sql.NullString

	if err := s.Scan(&d.ID, &d.OUISerial, &d.Manufacturer, &d.OUI, &d.ProductClass, &d.SerialNumber,
		&d.DataModelRoot, &d.OnlineStatus, &lastInformAt, &eventCodes,
		&connectionRequestURL, &d.ConnectionRequestMode, &lastConnectionRequestAt,
		&lastConnectionRequestStatus, &lastInformAfterCR,
		&d.FirstSeenAt, &d.LastUpdatedAt, &tags, &d.CWMPAuthMode, &dataModelRootConfirmedAt,
		&udpConnectionRequestAddress, &natDetected, &customerID, &location); err != nil {
		return nil, fmt.Errorf("scan device: %w", err)
	}
	if customerID.Valid {
		d.CustomerID = &customerID.String
	}
	if location.Valid {
		d.Location = &location.String
	}
	if dataModelRootConfirmedAt.Valid {
		t := dataModelRootConfirmedAt.Time
		d.DataModelRootConfirmedAt = &t
	}
	if udpConnectionRequestAddress.Valid {
		d.UDPConnectionRequestAddress = &udpConnectionRequestAddress.String
	}
	if natDetected.Valid {
		d.NATDetected = &natDetected.Bool
	}
	d.Tags = []string(tags)
	if lastInformAt.Valid {
		t := lastInformAt.Time
		d.LastInformAt = &t
	}
	if connectionRequestURL.Valid {
		d.ConnectionRequestURL = &connectionRequestURL.String
	}
	if lastConnectionRequestAt.Valid {
		t := lastConnectionRequestAt.Time
		d.LastConnectionRequestAt = &t
	}
	if lastConnectionRequestStatus.Valid {
		d.LastConnectionRequestStatus = &lastConnectionRequestStatus.String
	}
	if lastInformAfterCR.Valid {
		t := lastInformAfterCR.Time
		d.LastInformAfterConnectionRequestAt = &t
	}
	d.LastInformEventCodes = []string(eventCodes)
	return &d, nil
}
