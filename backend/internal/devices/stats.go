// Fleet aggregates, scoped listings, and report rows (split out of
// repository.go, audit P3.1). scopeFilter is the one tenancy predicate
// every aggregate here shares.
package devices

import (
	"acs/internal/store"
	"context"
	"fmt"
	"strings"
)

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
func (r *Repository) Summary(ctx context.Context, customerIDs []string, scoped bool) ([]GroupCount, error) {
	clause, arg := scopeFilter(scoped, customerIDs)
	var args []any
	if arg != nil {
		args = append(args, arg)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT manufacturer, online_status, connection_request_mode, COUNT(*)
		FROM devices `+clause+`
		GROUP BY manufacturer, online_status, connection_request_mode
		ORDER BY manufacturer, online_status, connection_request_mode
	`, args...)
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
func (r *Repository) MatchingIDs(ctx context.Context, filter MatchingFilter, customerIDs []string, scoped bool) ([]string, error) {
	var conditions []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if scoped {
		conditions = append(conditions, "customer_id::text = ANY("+arg(store.StringArray(customerIDs))+")")
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
