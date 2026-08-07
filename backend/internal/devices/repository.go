package devices

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

// GroupCount is one row of Summary's output.
type GroupCount struct {
	Manufacturer          string
	OnlineStatus          string
	ConnectionRequestMode string
	Count                 int
}

// Summary returns fleet counts grouped by manufacturer/status/reachability
// — a SQL GROUP BY, not "fetch everything and count in the frontend", so
// it stays cheap regardless of fleet size. This is what a mass-review
// view can render immediately without paging through every device first.
func (r *Repository) Summary(ctx context.Context) ([]GroupCount, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT manufacturer, online_status, connection_request_mode, COUNT(*)
		FROM devices
		GROUP BY manufacturer, online_status, connection_request_mode
		ORDER BY manufacturer, online_status, connection_request_mode
	`)
	if err != nil {
		return nil, fmt.Errorf("summarize devices: %w", err)
	}
	defer rows.Close()

	var out []GroupCount
	for rows.Next() {
		var g GroupCount
		if err := rows.Scan(&g.Manufacturer, &g.OnlineStatus, &g.ConnectionRequestMode, &g.Count); err != nil {
			return nil, fmt.Errorf("scan group count: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
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

// scopeFilter is the multi-tenancy WHERE-clause fragment shared by every
// dashboard/fleet-health aggregate below — same customer_id::text = ANY($1)
// shape List uses (see ListParams' doc comment for why text, not uuid[]).
// Returns "" and a nil arg when unscoped, so callers can unconditionally
// splice the clause into their query without an if/else at each call site.
func scopeFilter(scoped bool, customerIDs []string) (clause string, arg any) {
	if !scoped {
		return "", nil
	}
	return "WHERE customer_id::text = ANY($1)", store.StringArray(customerIDs)
}

// CountByOnlineStatus is the coarse aggregate cmd/acs polls into the
// acs_devices_online gauge (build plan §4 Phase 7 dashboards) — the
// per-manufacturer/mode breakdown Summary gives the Fleet Control screen
// is more than a metrics scrape needs. scoped/customerIDs apply
// multi-tenancy scoping (admin-platform backlog) — pass scoped=false for
// the unrestricted fleet-wide view.
func (r *Repository) CountByOnlineStatus(ctx context.Context, customerIDs []string, scoped bool) (map[string]int, error) {
	clause, arg := scopeFilter(scoped, customerIDs)
	var args []any
	if arg != nil {
		args = append(args, arg)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT online_status, COUNT(*) FROM devices `+clause+` GROUP BY online_status`, args...)
	if err != nil {
		return nil, fmt.Errorf("count devices by online status: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan online status count: %w", err)
		}
		out[status] = count
	}
	return out, rows.Err()
}

// maxMatchingIDs caps MatchingIDs' result — a "select all" is meant to
// stand in for paging through Fleet Control's filtered view by hand, not
// to hand a caller an unbounded fleet-wide ID dump.
const maxMatchingIDs = 5000

// MatchingFilter is MatchingIDs' input — the same filters Fleet Control's
// grouped summary strip and free-text search already express client-side,
// mirrored server-side so "select all N matching this filter" doesn't need
// the client to have paged through every device first.
type MatchingFilter struct {
	Manufacturer          string
	OnlineStatus          string
	ConnectionRequestMode string
	Search                string
}

// MatchingIDs returns every device ID matching filter, capped at
// maxMatchingIDs. Build plan §6.2's stated scope boundary: Fleet Control's
// row selection accumulated across pages but had no way to select
// everything matching a filter without paging through it by hand — this
// is that "real further step".
func (r *Repository) MatchingIDs(ctx context.Context, filter MatchingFilter) ([]string, error) {
	var conditions []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.Manufacturer != "" {
		conditions = append(conditions, "manufacturer = "+arg(filter.Manufacturer))
	}
	if filter.OnlineStatus != "" {
		conditions = append(conditions, "online_status = "+arg(filter.OnlineStatus))
	}
	if filter.ConnectionRequestMode != "" {
		conditions = append(conditions, "connection_request_mode = "+arg(filter.ConnectionRequestMode))
	}
	if filter.Search != "" {
		conditions = append(conditions, "(oui_serial ILIKE "+arg("%"+filter.Search+"%")+" OR manufacturer ILIKE "+arg("%"+filter.Search+"%")+" OR product_class ILIKE "+arg("%"+filter.Search+"%")+")")
	}

	query := "SELECT id FROM devices"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += fmt.Sprintf(" ORDER BY last_inform_at DESC NULLS LAST LIMIT %d", maxMatchingIDs)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("matching device ids: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan matching device id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CountByReachability is CountByOnlineStatus's counterpart for Fleet
// Health's reachability breakdown (design doc v3 §16.1's "connection
// request success rate" — the per-device connection_request_mode already
// records the same signal, aggregated here rather than duplicated).
func (r *Repository) CountByReachability(ctx context.Context, customerIDs []string, scoped bool) (map[string]int, error) {
	clause, arg := scopeFilter(scoped, customerIDs)
	var args []any
	if arg != nil {
		args = append(args, arg)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT connection_request_mode, COUNT(*) FROM devices `+clause+` GROUP BY connection_request_mode`, args...)
	if err != nil {
		return nil, fmt.Errorf("count devices by reachability: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var mode string
		var count int
		if err := rows.Scan(&mode, &count); err != nil {
			return nil, fmt.Errorf("scan reachability count: %w", err)
		}
		out[mode] = count
	}
	return out, rows.Err()
}

// InformRecencyBuckets buckets devices by how long ago their last Inform
// was seen — Fleet Health's "inform rate" signal (design doc v3 §16.1),
// computed as a live histogram rather than a single stale/fresh boolean so
// a slow fleet-wide drift is visible before every device tips into
// "stale".
func (r *Repository) InformRecencyBuckets(ctx context.Context, customerIDs []string, scoped bool) (map[string]int, error) {
	clause, arg := scopeFilter(scoped, customerIDs)
	var args []any
	if arg != nil {
		args = append(args, arg)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			CASE
				WHEN last_inform_at IS NULL THEN 'never'
				WHEN last_inform_at > now() - interval '5 minutes' THEN 'under_5m'
				WHEN last_inform_at > now() - interval '1 hour' THEN 'under_1h'
				WHEN last_inform_at > now() - interval '24 hours' THEN 'under_24h'
				ELSE 'stale'
			END AS bucket,
			COUNT(*)
		FROM devices `+clause+`
		GROUP BY bucket
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("bucket inform recency: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var bucket string
		var count int
		if err := rows.Scan(&bucket, &count); err != nil {
			return nil, fmt.Errorf("scan inform recency bucket: %w", err)
		}
		out[bucket] = count
	}
	return out, rows.Err()
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

// ReportRow is one row of the Excel export (admin-platform backlog) —
// device status/identity plus the small set of cached parameters the
// user's report spec named (firmware version, current SSID, MAC).
// SoftwareVersion/SSID/MAC are "" when the CPE has never reported them,
// not fabricated.
type ReportRow struct {
	SerialNumber    string
	Manufacturer    string
	ProductClass    string
	OnlineStatus    string
	Location        string
	CustomerName    string
	RegionName      string
	SoftwareVersion string
	SSID            string
	MACAddress      string
}

// ReportRows backs the Excel export. customerIDs/scoped is the calling
// operator's multi-tenancy scope (always applied); filterCustomerID/
// filterRegionID/filterProjectID further narrow the report to one
// customer/region/project on top of that, all optional and combinable.
func (r *Repository) ReportRows(ctx context.Context, customerIDs []string, scoped bool, filterCustomerID, filterRegionID, filterProjectID *string) ([]ReportRow, error) {
	var conditions []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if scoped {
		conditions = append(conditions, "d.customer_id::text = ANY("+arg(store.StringArray(customerIDs))+")")
	}
	if filterCustomerID != nil && *filterCustomerID != "" {
		conditions = append(conditions, "d.customer_id = "+arg(*filterCustomerID))
	}
	if filterRegionID != nil && *filterRegionID != "" {
		conditions = append(conditions, "reg.id = "+arg(*filterRegionID))
	}
	if filterProjectID != nil && *filterProjectID != "" {
		conditions = append(conditions, "EXISTS (SELECT 1 FROM device_projects dp WHERE dp.device_id = d.id AND dp.project_id = "+arg(*filterProjectID)+")")
	}

	query := `
		SELECT d.serial_number, d.manufacturer, d.product_class, d.online_status,
			COALESCE(d.location, ''), COALESCE(c.name, ''), COALESCE(reg.name, ''),
			COALESCE(p.parameters->'Device.DeviceInfo.SoftwareVersion'->>'value', ''),
			COALESCE(p.parameters->'Device.WiFi.SSID.1.SSID'->>'value', ''),
			COALESCE((SELECT kv.value->>'value' FROM jsonb_each(p.parameters) kv WHERE kv.key ILIKE '%MACAddress%' LIMIT 1), '')
		FROM devices d
		LEFT JOIN customers c ON c.id = d.customer_id
		LEFT JOIN regions reg ON reg.id = c.region_id
		LEFT JOIN device_parameter_cache p ON p.device_id = d.id
	`
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY d.oui_serial"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query report rows: %w", err)
	}
	defer rows.Close()

	var out []ReportRow
	for rows.Next() {
		var row ReportRow
		if err := rows.Scan(&row.SerialNumber, &row.Manufacturer, &row.ProductClass, &row.OnlineStatus,
			&row.Location, &row.CustomerName, &row.RegionName, &row.SoftwareVersion, &row.SSID, &row.MACAddress); err != nil {
			return nil, fmt.Errorf("scan report row: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// CountByCustomer groups devices by owning customer (admin-platform
// backlog: dashboard "group by" widget) — "Unassigned" covers devices with
// no customer_id, same as a pre-registered/bulk-imported device that
// hasn't been assigned yet.
func (r *Repository) CountByCustomer(ctx context.Context, customerIDs []string, scoped bool) (map[string]int, error) {
	clause, arg := scopeFilter(scoped, customerIDs)
	if clause != "" {
		clause = strings.Replace(clause, "WHERE", "WHERE d.", 1) // d.customer_id, see the join below
	}
	var args []any
	if arg != nil {
		args = append(args, arg)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT COALESCE(c.name, 'Unassigned'), COUNT(*)
		FROM devices d LEFT JOIN customers c ON c.id = d.customer_id
		`+clause+`
		GROUP BY c.name
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("count devices by customer: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, fmt.Errorf("scan customer count: %w", err)
		}
		out[name] = count
	}
	return out, rows.Err()
}

// CountByManufacturer groups devices by manufacturer — the simplest
// dashboard "group by" dimension, no join required.
func (r *Repository) CountByManufacturer(ctx context.Context, customerIDs []string, scoped bool) (map[string]int, error) {
	clause, arg := scopeFilter(scoped, customerIDs)
	var args []any
	if arg != nil {
		args = append(args, arg)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT manufacturer, COUNT(*) FROM devices `+clause+` GROUP BY manufacturer`, args...)
	if err != nil {
		return nil, fmt.Errorf("count devices by manufacturer: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, fmt.Errorf("scan manufacturer count: %w", err)
		}
		out[name] = count
	}
	return out, rows.Err()
}

// DeviceVersion is one row of SoftwareVersionsByModel's result — enough to
// look up the latest known firmware for this manufacturer+model and decide
// whether the device is behind (admin-platform backlog: "firmware upgrade
// available" dashboard widget).
type DeviceVersion struct {
	DeviceID        string
	Manufacturer    string
	ProductClass    string
	SoftwareVersion string // "" if never reported (no GetParameterValues/Inform has carried it yet)
}

// SoftwareVersionsByModel reads each in-scope device's cached
// Device.DeviceInfo.SoftwareVersion — the `->`/`->>` JSONB descent
// pattern already used by internal/rollout for the same parameter.
func (r *Repository) SoftwareVersionsByModel(ctx context.Context, customerIDs []string, scoped bool) ([]DeviceVersion, error) {
	clause, arg := scopeFilter(scoped, customerIDs)
	if clause != "" {
		clause = strings.Replace(clause, "WHERE", "WHERE d.", 1)
	}
	var args []any
	if arg != nil {
		args = append(args, arg)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT d.id, d.manufacturer, d.product_class,
			COALESCE(p.parameters->'Device.DeviceInfo.SoftwareVersion'->>'value', '')
		FROM devices d LEFT JOIN device_parameter_cache p ON p.device_id = d.id
		`+clause+`
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("read device software versions: %w", err)
	}
	defer rows.Close()

	var out []DeviceVersion
	for rows.Next() {
		var v DeviceVersion
		if err := rows.Scan(&v.DeviceID, &v.Manufacturer, &v.ProductClass, &v.SoftwareVersion); err != nil {
			return nil, fmt.Errorf("scan device software version: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
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

// UpdateConnectionRequestURL records the ManagementServer.ConnectionRequestURL
// a device reported on Inform (build plan §4 Phase 3). Called on every
// Inform that carries the parameter; a no-op write when it hasn't
// changed is fine — Inform-frequency writes to one row are not a
// meaningful cost here.
func (r *Repository) UpdateConnectionRequestURL(ctx context.Context, deviceID, url string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET connection_request_url = $2, last_updated_at = now()
		WHERE id = $1
	`, deviceID, url)
	if err != nil {
		return fmt.Errorf("update connection request url: %w", err)
	}
	return nil
}

// UpdateDataModelRoot records the data model root confirmed by a successful
// parameter discovery run (build plan nice-to-have backlog) — the first
// time this column moves off UNKNOWN in production, since UpsertFromInform
// deliberately never touches it (see that method's doc comment).
func (r *Repository) UpdateDataModelRoot(ctx context.Context, deviceID, root string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET data_model_root = $2, data_model_root_confirmed_at = now(), last_updated_at = now()
		WHERE id = $1
	`, deviceID, root)
	if err != nil {
		return fmt.Errorf("update data model root: %w", err)
	}
	return nil
}

// UpdateSTUNStatus records what a device reported about its own STUN/NAT
// state on Inform (critical feature backlog: STUN NAT traversal). When the
// CPE itself reports NATDetected=true, connection_request_mode is also
// classified as STUN_ANNEX_G — a real device behind CGNAT reporting a
// working STUN binding is a strictly better-informed status than
// UNKNOWN/PERIODIC_FALLBACK, so it's fine to overwrite those; a mode of
// DIRECT_IPV4/DIRECT_IPV6 (a Connection Request that already succeeded
// directly) is left untouched since direct reachability is strictly better
// than STUN and shouldn't be downgraded by this.
func (r *Repository) UpdateSTUNStatus(ctx context.Context, deviceID, udpConnectionRequestAddress string, natDetected bool) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET
			udp_connection_request_address = $2,
			nat_detected = $3,
			connection_request_mode = CASE
				WHEN $3 AND connection_request_mode NOT IN ('DIRECT_IPV4', 'DIRECT_IPV6') THEN 'STUN_ANNEX_G'
				ELSE connection_request_mode
			END,
			last_updated_at = now()
		WHERE id = $1
	`, deviceID, nullIfEmpty(udpConnectionRequestAddress), natDetected)
	if err != nil {
		return fmt.Errorf("update stun status: %w", err)
	}
	return nil
}

// RecordConnectionRequestAttempt records the outcome of a Connection
// Request attempt (design doc v3 §12.3). mode is optional — pass "" to
// leave the device's current reachability classification untouched
// (e.g. on a single transient HTTP failure, where downgrading the mode
// would just cause flapping — see build plan Phase 3 design notes).
func (r *Repository) RecordConnectionRequestAttempt(ctx context.Context, deviceID, status, mode string) error {
	if mode == "" {
		_, err := r.db.ExecContext(ctx, `
			UPDATE devices SET last_connection_request_at = now(), last_connection_request_status = $2
			WHERE id = $1
		`, deviceID, status)
		if err != nil {
			return fmt.Errorf("record connection request attempt: %w", err)
		}
		return nil
	}

	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET last_connection_request_at = now(), last_connection_request_status = $2, connection_request_mode = $3
		WHERE id = $1
	`, deviceID, status, mode)
	if err != nil {
		return fmt.Errorf("record connection request attempt: %w", err)
	}
	return nil
}

// MarkInformedAfterConnectionRequest records that a device sent an Inform
// (with CONNECTION REQUEST among its event codes) after a Connection
// Request attempt — the confirmation the connreq worker is waiting for.
func (r *Repository) MarkInformedAfterConnectionRequest(ctx context.Context, deviceID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET last_inform_after_connection_request_at = now() WHERE id = $1
	`, deviceID)
	if err != nil {
		return fmt.Errorf("mark informed after connection request: %w", err)
	}
	return nil
}

// InformedWithEventSince reports whether the device has sent an Inform
// carrying the given event code (e.g. "6 CONNECTION REQUEST") at or after
// the given time — how the connreq worker detects that its Connection
// Request actually provoked a new session, as opposed to a coincidental
// periodic Inform racing in during the wait window.
func (r *Repository) InformedWithEventSince(ctx context.Context, deviceID string, since time.Time, eventCodePrefix string) (bool, error) {
	var lastInformAt sql.NullTime
	var eventCodes store.StringArray
	err := r.db.QueryRowContext(ctx, `
		SELECT last_inform_at, last_inform_event_codes FROM devices WHERE id = $1
	`, deviceID).Scan(&lastInformAt, &eventCodes)
	if err != nil {
		return false, fmt.Errorf("check informed since: %w", err)
	}
	if !lastInformAt.Valid || lastInformAt.Time.Before(since) {
		return false, nil
	}
	for _, code := range eventCodes {
		if strings.HasPrefix(code, eventCodePrefix) {
			return true, nil
		}
	}
	return false, nil
}
