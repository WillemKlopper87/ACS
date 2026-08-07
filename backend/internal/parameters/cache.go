// Package parameters owns the per-device parameter cache (design doc v3
// §7.7 / build plan §4 Phase 2). This is a cache of what the CPE last
// reported, not live state — every cached value carries an as_of
// timestamp and source so a caller can judge freshness (v3 §19.6/§19.8:
// "Do not trust cached parameters as live state").
package parameters

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"acs/internal/store"
)

const (
	SourceInform    = "INFORM"
	SourceGetValues = "GET_PARAMETER_VALUES"
	SourceSetValues = "SET_PARAMETER_VALUES"
)

// CachedValue is one entry in a device's parameter cache.
type CachedValue struct {
	Value     string    `json:"value"`
	Type      string    `json:"type,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    string    `json:"source"`
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Upsert merges the given parameters into the device's cache. It uses
// Postgres's jsonb `||` concatenation operator so a partial update (e.g.
// a GetParameterValues response covering only the parameters just
// written) does not erase unrelated cached values.
//
// Also appends to parameter_history — but only for names whose value
// actually changed from what was previously cached, not on every call
// (build plan nice-to-have backlog: "parameter value history" was
// explicitly not a table separate from the latest-value cache before
// this). Diff-then-write happens inside the same transaction as the
// cache upsert so the two never disagree about what "changed" means.
func (r *Repository) Upsert(ctx context.Context, deviceID string, values map[string]CachedValue) error {
	if len(values) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin parameter cache upsert tx: %w", err)
	}
	defer tx.Rollback()

	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT parameters FROM device_parameter_cache WHERE device_id = $1 FOR UPDATE`, deviceID).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("load current parameter cache: %w", err)
	}
	current := map[string]CachedValue{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &current); err != nil {
			return fmt.Errorf("unmarshal current parameter cache: %w", err)
		}
	}

	for name, v := range values {
		if prev, ok := current[name]; ok && prev.Value == v.Value {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO parameter_history (device_id, name, value, type, source, recorded_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, deviceID, name, v.Value, nullIfEmpty(v.Type), v.Source, v.UpdatedAt); err != nil {
			return fmt.Errorf("insert parameter history: %w", err)
		}
	}

	patch, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("marshal parameter cache patch: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO device_parameter_cache (device_id, parameters, updated_at)
		VALUES ($1, $2::jsonb, now())
		ON CONFLICT (device_id) DO UPDATE SET
			parameters = device_parameter_cache.parameters || EXCLUDED.parameters,
			updated_at = now()
	`, deviceID, patch); err != nil {
		return fmt.Errorf("upsert parameter cache: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit parameter cache upsert tx: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// History returns a parameter's recorded value history, most recent
// first, capped at historyLimit.
const historyLimit = 200

// HistoryEntry is one row of parameter_history.
type HistoryEntry struct {
	Value      string
	Type       string
	Source     string
	RecordedAt time.Time
}

func (r *Repository) History(ctx context.Context, deviceID, name string) ([]HistoryEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT value, COALESCE(type, ''), source, recorded_at
		FROM parameter_history
		WHERE device_id = $1 AND name = $2
		ORDER BY recorded_at DESC
		LIMIT $3`, deviceID, name, historyLimit)
	if err != nil {
		return nil, fmt.Errorf("query parameter history: %w", err)
	}
	defer rows.Close()

	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.Value, &e.Type, &e.Source, &e.RecordedAt); err != nil {
			return nil, fmt.Errorf("scan parameter history: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns the full cached parameter map for a device (empty map, not
// an error, if the device has no cached parameters yet).
func (r *Repository) Get(ctx context.Context, deviceID string) (map[string]CachedValue, error) {
	var raw []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT parameters FROM device_parameter_cache WHERE device_id = $1`, deviceID,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return map[string]CachedValue{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get parameter cache: %w", err)
	}

	values := map[string]CachedValue{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("unmarshal parameter cache: %w", err)
		}
	}
	return values, nil
}

// TemperatureReading is one cached parameter whose name suggests it
// reports a temperature — admin-platform backlog's dashboard "temperature
// high/low" widget. Deliberately generic (ILIKE '%Temperature%' rather
// than one hardcoded TR-181 path like
// Device.DeviceInfo.TemperatureStatus.TemperatureSensor.1.Value) since
// which exact parameter a given vendor reports varies; this surfaces
// whatever's actually in the cache rather than assuming a path no device
// in this fleet may even implement. Value is left as the raw cached
// string — TR-069 doesn't guarantee it's numeric-only (a vendor could
// report "42.5 C"), so parsing/validating it is left to the caller.
type TemperatureReading struct {
	DeviceID      string
	ParameterName string
	Value         string
}

// TemperatureReadings scans every in-scope device's cached parameters for
// keys mentioning "Temperature". customerIDs/scoped mirror
// devices.ListParams' multi-tenancy scoping.
func (r *Repository) TemperatureReadings(ctx context.Context, customerIDs []string, scoped bool) ([]TemperatureReading, error) {
	clause := ""
	var args []any
	if scoped {
		clause = "AND d.customer_id::text = ANY($1)"
		args = append(args, store.StringArray(customerIDs))
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.device_id, kv.key, kv.value->>'value'
		FROM device_parameter_cache p
		JOIN devices d ON d.id = p.device_id
		, jsonb_each(p.parameters) kv
		WHERE kv.key ILIKE '%Temperature%' `+clause+`
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("scan temperature readings: %w", err)
	}
	defer rows.Close()

	var out []TemperatureReading
	for rows.Next() {
		var t TemperatureReading
		if err := rows.Scan(&t.DeviceID, &t.ParameterName, &t.Value); err != nil {
			return nil, fmt.Errorf("scan temperature reading: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveNames replaces a device's discovered parameter-name tree wholesale —
// see 0027_parameter_discovery.sql for why this is a full replace rather
// than the merge Upsert does for values.
func (r *Repository) SaveNames(ctx context.Context, deviceID string, writableByName map[string]bool) error {
	patch, err := json.Marshal(writableByName)
	if err != nil {
		return fmt.Errorf("marshal discovered parameter names: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO device_parameter_names (device_id, names, discovered_at)
		VALUES ($1, $2::jsonb, now())
		ON CONFLICT (device_id) DO UPDATE SET
			names = EXCLUDED.names,
			discovered_at = now()
	`, deviceID, patch)
	if err != nil {
		return fmt.Errorf("save discovered parameter names: %w", err)
	}
	return nil
}

// DiscoveredNames is one device's stored parameter-name tree plus when it
// was last (re)discovered.
type DiscoveredNames struct {
	Names        map[string]bool
	DiscoveredAt time.Time
}

// GetNames returns a device's discovered parameter-name tree (nil Names, no
// error, if discovery has never run for this device).
func (r *Repository) GetNames(ctx context.Context, deviceID string) (*DiscoveredNames, error) {
	var raw []byte
	var discoveredAt time.Time
	err := r.db.QueryRowContext(ctx,
		`SELECT names, discovered_at FROM device_parameter_names WHERE device_id = $1`, deviceID,
	).Scan(&raw, &discoveredAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get discovered parameter names: %w", err)
	}

	names := map[string]bool{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &names); err != nil {
			return nil, fmt.Errorf("unmarshal discovered parameter names: %w", err)
		}
	}
	return &DiscoveredNames{Names: names, DiscoveredAt: discoveredAt}, nil
}
