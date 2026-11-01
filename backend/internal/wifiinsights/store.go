package wifiinsights

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// DefaultSessionGap closes a client session when the client was absent for
// longer than this. Three missed 5-minute samples is long enough to ride
// out one dropped CWMP session.
const DefaultSessionGap = 15 * time.Minute

// Repository stores samples and serves the insights queries.
type Repository struct {
	db  *sql.DB
	oui *OUIRegistry
}

func NewRepository(db *sql.DB, oui *OUIRegistry) *Repository {
	return &Repository{db: db, oui: oui}
}

// LastSampleAt returns the device's most recent sample time (zero if none).
func (r *Repository) LastSampleAt(ctx context.Context, deviceID string) (time.Time, error) {
	var at sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT MAX(sampled_at) FROM wifi_samples WHERE device_id=$1`, deviceID).Scan(&at)
	if err != nil {
		return time.Time{}, err
	}
	return at.Time, nil
}

// Record stores one snapshot taken at `at` and updates the client registry.
func (r *Repository) Record(ctx context.Context, deviceID string, at time.Time, snap Snapshot, sessionGap time.Duration) error {
	if sessionGap <= 0 {
		sessionGap = DefaultSessionGap
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin wifi sample tx: %w", err)
	}
	defer tx.Rollback()

	wireless, wired, guest := snap.Counts()
	var sampleID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO wifi_samples (device_id, sampled_at, wireless_clients, wired_clients, guest_clients)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (device_id, sampled_at) DO NOTHING
		RETURNING id`, deviceID, at, wireless, wired, guest).Scan(&sampleID)
	if err == sql.ErrNoRows {
		return nil // already recorded
	}
	if err != nil {
		return fmt.Errorf("insert wifi sample: %w", err)
	}
	// Mesh nodes are never clients: drop what was stored for them before
	// they were recognised (field data 2026-10-30: the Beacon topped the
	// FastMile's client list), so every window stops listing them.
	if len(snap.MeshMACs) > 0 {
		for _, table := range []string{"wifi_client_samples", "wifi_clients"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE device_id = $1 AND mac = ANY($2)`, deviceID, snap.MeshMACs); err != nil {
				return fmt.Errorf("drop mesh node rows from %s: %w", table, err)
			}
		}
	}
	for _, s := range snap.SSIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO wifi_ssid_samples (sample_id, device_id, sampled_at, ssid_instance, ssid_name, role, bytes_down, bytes_up)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8)`,
			sampleID, deviceID, at, s.Instance, s.Name, s.Role, s.BytesDown, s.BytesUp); err != nil {
			return fmt.Errorf("insert wifi ssid sample: %w", err)
		}
	}
	gapSeconds := sessionGap.Seconds()
	for _, c := range snap.Clients {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO wifi_client_samples (sample_id, device_id, sampled_at, mac, ssid_instance, signal_dbm, bytes_down, bytes_up)
			VALUES ($1, $2, $3, $4, NULLIF($5, 0), $6, $7, $8)`,
			sampleID, deviceID, at, c.MAC, c.SSID, c.SignalDBm, c.BytesDown, c.BytesUp); err != nil {
			return fmt.Errorf("insert wifi client sample: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO wifi_clients (device_id, mac, first_seen_at, last_seen_at, session_started_at, sessions, connected_seconds,
				hostname, ip_address, ssid_instance, ssid_name, band, mesh_node, wireless, guest, private_mac, vendor, signal_dbm)
			VALUES ($1, $2, $3, $3, $3, 1, 0, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, 0), NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, 0), $10, $11, $12, NULLIF($13, ''), $14)
			ON CONFLICT (device_id, mac) DO UPDATE SET
				sessions = wifi_clients.sessions + CASE WHEN $3 - wifi_clients.last_seen_at > make_interval(secs => $15) THEN 1 ELSE 0 END,
				session_started_at = CASE WHEN $3 - wifi_clients.last_seen_at > make_interval(secs => $15) THEN $3 ELSE wifi_clients.session_started_at END,
				connected_seconds = wifi_clients.connected_seconds + CASE
					WHEN $3 <= wifi_clients.last_seen_at OR $3 - wifi_clients.last_seen_at > make_interval(secs => $15) THEN 0
					ELSE EXTRACT(EPOCH FROM $3 - wifi_clients.last_seen_at)::BIGINT END,
				last_seen_at = GREATEST(wifi_clients.last_seen_at, $3),
				hostname = COALESCE(EXCLUDED.hostname, wifi_clients.hostname),
				ip_address = COALESCE(EXCLUDED.ip_address, wifi_clients.ip_address),
				ssid_instance = COALESCE(EXCLUDED.ssid_instance, wifi_clients.ssid_instance),
				ssid_name = COALESCE(EXCLUDED.ssid_name, wifi_clients.ssid_name),
				band = COALESCE(EXCLUDED.band, wifi_clients.band),
				mesh_node = COALESCE(EXCLUDED.mesh_node, wifi_clients.mesh_node),
				wireless = EXCLUDED.wireless,
				guest = EXCLUDED.guest,
				vendor = COALESCE(EXCLUDED.vendor, wifi_clients.vendor),
				signal_dbm = EXCLUDED.signal_dbm`,
			deviceID, c.MAC, at, c.Hostname, c.IPAddress, c.SSID, c.SSIDName, c.Band, c.MeshNode,
			c.Wireless, c.Guest, PrivateMAC(c.MAC), r.oui.Vendor(c.MAC), c.SignalDBm, gapSeconds); err != nil {
			return fmt.Errorf("upsert wifi client: %w", err)
		}
	}
	return tx.Commit()
}

// Window is a supported insights period, matching the GDMS selector.
type Window struct {
	Name   string
	Span   time.Duration
	Bucket time.Duration
}

var Windows = map[string]Window{
	"2h": {"2h", 2 * time.Hour, 5 * time.Minute},
	"1d": {"1d", 24 * time.Hour, time.Hour},
	"1w": {"1w", 7 * 24 * time.Hour, 6 * time.Hour},
	"1m": {"1m", 30 * 24 * time.Hour, 24 * time.Hour},
}

type BandwidthPoint struct {
	At        time.Time `json:"at"`
	BytesDown int64     `json:"bytes_down"`
	BytesUp   int64     `json:"bytes_up"`
	// Average rates over the bucket, in bits per second.
	BpsDown float64 `json:"bps_down"`
	BpsUp   float64 `json:"bps_up"`
}

type ClientCountPoint struct {
	At       time.Time `json:"at"`
	Wireless int       `json:"wireless"`
	Wired    int       `json:"wired"`
	Guest    int       `json:"guest"`
}

type SSIDUsage struct {
	Instance  int    `json:"instance"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	BytesDown int64  `json:"bytes_down"`
	BytesUp   int64  `json:"bytes_up"`
}

type ClientUsage struct {
	MAC       string `json:"mac"`
	Hostname  string `json:"hostname"`
	Vendor    string `json:"vendor"`
	BytesDown int64  `json:"bytes_down"`
	BytesUp   int64  `json:"bytes_up"`
}

type ClientSummary struct {
	MAC              string    `json:"mac"`
	Hostname         string    `json:"hostname"`
	IPAddress        string    `json:"ip_address"`
	SSIDInstance     int       `json:"ssid_instance,omitempty"`
	SSIDName         string    `json:"ssid_name"`
	Band             string    `json:"band"`
	MeshNode         int       `json:"mesh_node,omitempty"`
	Wireless         bool      `json:"wireless"`
	Guest            bool      `json:"guest"`
	PrivateMAC       bool      `json:"private_mac"`
	Vendor           string    `json:"vendor"`
	OSInferred       string    `json:"os_inferred"`
	SignalDBm        *int      `json:"signal_dbm"`
	FirstSeenAt      time.Time `json:"first_seen_at"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	Connected        bool      `json:"connected"`
	Sessions         int       `json:"sessions"`
	ConnectedSeconds int64     `json:"connected_seconds"`
}

type Share struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type ClientStats struct {
	New       int `json:"new"`
	Returning int `json:"returning"`
	Private   int `json:"private_mac"`
	// AverageSessionSeconds is connected time per session over each
	// client's whole history (not only the window).
	AverageSessionSeconds int64 `json:"average_session_seconds"`
}

type Overview struct {
	DeviceID     string             `json:"device_id"`
	Window       string             `json:"window"`
	Since        time.Time          `json:"since"`
	BucketSecs   int                `json:"bucket_seconds"`
	LastSampleAt *time.Time         `json:"last_sample_at"`
	TotalDown    int64              `json:"total_bytes_down"`
	TotalUp      int64              `json:"total_bytes_up"`
	Bandwidth    []BandwidthPoint   `json:"bandwidth"`
	ClientCounts []ClientCountPoint `json:"client_counts"`
	TopSSIDs     []SSIDUsage        `json:"top_ssids"`
	TopClients   []ClientUsage      `json:"top_clients"`
	ClientStats  ClientStats        `json:"client_stats"`
	Vendors      []Share            `json:"vendors"`
	OS           []Share            `json:"os_inferred"`
	Clients      []ClientSummary    `json:"clients"`
}

const topLimit = 10

// counterDeltas is the shared CTE: per-series positive differences between
// consecutive samples. The extra day of look-back finds the sample before
// the window so its first bucket has a delta; negative differences (counter
// reset on reboot or re-association) are dropped.
const counterDeltas = `
	SELECT sampled_at, %[1]s AS series,
		bytes_down - LAG(bytes_down) OVER w AS dd,
		bytes_up - LAG(bytes_up) OVER w AS du
	FROM %[2]s
	WHERE device_id = $1 AND sampled_at >= $2::timestamptz - interval '1 day' AND sampled_at <= $3
	WINDOW w AS (PARTITION BY %[1]s ORDER BY sampled_at)`

// Overview answers the insights page for one device and window.
func (r *Repository) Overview(ctx context.Context, deviceID string, w Window, now time.Time) (*Overview, error) {
	since := now.Add(-w.Span)
	out := &Overview{DeviceID: deviceID, Window: w.Name, Since: since, BucketSecs: int(w.Bucket.Seconds()),
		Bandwidth: []BandwidthPoint{}, ClientCounts: []ClientCountPoint{}, TopSSIDs: []SSIDUsage{}, TopClients: []ClientUsage{},
		Vendors: []Share{}, OS: []Share{}, Clients: []ClientSummary{}}
	last, err := r.LastSampleAt(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if !last.IsZero() {
		out.LastSampleAt = &last
	}

	rows, err := r.db.QueryContext(ctx, `WITH d AS (`+fmt.Sprintf(counterDeltas, "ssid_instance", "wifi_ssid_samples")+`)
		SELECT date_bin(make_interval(secs => $4), sampled_at, $2::timestamptz) AS b,
			COALESCE(SUM(GREATEST(dd, 0)), 0), COALESCE(SUM(GREATEST(du, 0)), 0)
		FROM d WHERE sampled_at >= $2 AND dd IS NOT NULL
		GROUP BY b ORDER BY b`, deviceID, since, now, w.Bucket.Seconds())
	if err != nil {
		return nil, fmt.Errorf("bandwidth: %w", err)
	}
	for rows.Next() {
		var p BandwidthPoint
		if err := rows.Scan(&p.At, &p.BytesDown, &p.BytesUp); err != nil {
			rows.Close()
			return nil, err
		}
		p.BpsDown = float64(p.BytesDown*8) / w.Bucket.Seconds()
		p.BpsUp = float64(p.BytesUp*8) / w.Bucket.Seconds()
		out.TotalDown += p.BytesDown
		out.TotalUp += p.BytesUp
		out.Bandwidth = append(out.Bandwidth, p)
	}
	rows.Close()

	rows, err = r.db.QueryContext(ctx, `
		SELECT date_bin(make_interval(secs => $4), sampled_at, $2::timestamptz) AS b,
			MAX(wireless_clients), MAX(wired_clients), MAX(guest_clients)
		FROM wifi_samples WHERE device_id = $1 AND sampled_at >= $2 AND sampled_at <= $3
		GROUP BY b ORDER BY b`, deviceID, since, now, w.Bucket.Seconds())
	if err != nil {
		return nil, fmt.Errorf("client counts: %w", err)
	}
	for rows.Next() {
		var p ClientCountPoint
		if err := rows.Scan(&p.At, &p.Wireless, &p.Wired, &p.Guest); err != nil {
			rows.Close()
			return nil, err
		}
		out.ClientCounts = append(out.ClientCounts, p)
	}
	rows.Close()

	rows, err = r.db.QueryContext(ctx, `WITH d AS (`+fmt.Sprintf(counterDeltas, "ssid_instance", "wifi_ssid_samples")+`),
		names AS (SELECT DISTINCT ON (ssid_instance) ssid_instance, COALESCE(ssid_name, '') AS name, COALESCE(role, '') AS role
			FROM wifi_ssid_samples WHERE device_id = $1 ORDER BY ssid_instance, sampled_at DESC)
		SELECT d.series, n.name, n.role, SUM(GREATEST(dd, 0)) AS down, SUM(GREATEST(du, 0)) AS up
		FROM d JOIN names n ON n.ssid_instance = d.series
		WHERE d.sampled_at >= $2 AND dd IS NOT NULL
		GROUP BY d.series, n.name, n.role
		HAVING SUM(GREATEST(dd, 0)) + SUM(GREATEST(du, 0)) > 0
		ORDER BY SUM(GREATEST(dd, 0)) + SUM(GREATEST(du, 0)) DESC LIMIT `+fmt.Sprint(topLimit), deviceID, since, now)
	if err != nil {
		return nil, fmt.Errorf("top ssids: %w", err)
	}
	for rows.Next() {
		var s SSIDUsage
		if err := rows.Scan(&s.Instance, &s.Name, &s.Role, &s.BytesDown, &s.BytesUp); err != nil {
			rows.Close()
			return nil, err
		}
		out.TopSSIDs = append(out.TopSSIDs, s)
	}
	rows.Close()

	rows, err = r.db.QueryContext(ctx, `WITH d AS (`+fmt.Sprintf(counterDeltas, "mac", "wifi_client_samples")+`)
		SELECT d.series, COALESCE(c.hostname, ''), COALESCE(c.vendor, ''), SUM(GREATEST(dd, 0)) AS down, SUM(GREATEST(du, 0)) AS up
		FROM d LEFT JOIN wifi_clients c ON c.device_id = $1 AND c.mac = d.series
		WHERE d.sampled_at >= $2 AND dd IS NOT NULL
		GROUP BY d.series, c.hostname, c.vendor
		HAVING SUM(GREATEST(dd, 0)) + SUM(GREATEST(du, 0)) > 0
		ORDER BY SUM(GREATEST(dd, 0)) + SUM(GREATEST(du, 0)) DESC LIMIT `+fmt.Sprint(topLimit), deviceID, since, now)
	if err != nil {
		return nil, fmt.Errorf("top clients: %w", err)
	}
	for rows.Next() {
		var c ClientUsage
		if err := rows.Scan(&c.MAC, &c.Hostname, &c.Vendor, &c.BytesDown, &c.BytesUp); err != nil {
			rows.Close()
			return nil, err
		}
		out.TopClients = append(out.TopClients, c)
	}
	rows.Close()

	clients, err := r.clientsSeenSince(ctx, deviceID, since, last)
	if err != nil {
		return nil, err
	}
	out.Clients = clients
	out.ClientStats, out.Vendors, out.OS = summarise(clients, since)
	return out, nil
}

func (r *Repository) clientsSeenSince(ctx context.Context, deviceID string, since, lastSample time.Time) ([]ClientSummary, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT mac, COALESCE(hostname, ''), COALESCE(ip_address, ''), COALESCE(ssid_instance, 0), COALESCE(ssid_name, ''),
			COALESCE(band, ''), COALESCE(mesh_node, 0), wireless, guest, private_mac, COALESCE(vendor, ''), signal_dbm,
			first_seen_at, last_seen_at, sessions, connected_seconds
		FROM wifi_clients WHERE device_id = $1 AND last_seen_at >= $2
		ORDER BY last_seen_at DESC, mac`, deviceID, since)
	if err != nil {
		return nil, fmt.Errorf("clients: %w", err)
	}
	defer rows.Close()
	out := []ClientSummary{}
	for rows.Next() {
		var c ClientSummary
		var signal sql.NullInt64
		if err := rows.Scan(&c.MAC, &c.Hostname, &c.IPAddress, &c.SSIDInstance, &c.SSIDName, &c.Band, &c.MeshNode,
			&c.Wireless, &c.Guest, &c.PrivateMAC, &c.Vendor, &signal, &c.FirstSeenAt, &c.LastSeenAt, &c.Sessions, &c.ConnectedSeconds); err != nil {
			return nil, err
		}
		if signal.Valid {
			v := int(signal.Int64)
			c.SignalDBm = &v
		}
		c.OSInferred = InferOS(c.Hostname)
		c.Connected = !lastSample.IsZero() && !c.LastSeenAt.Before(lastSample)
		out = append(out, c)
	}
	return out, rows.Err()
}

// summarise derives the client statistics and vendor/OS shares.
func summarise(clients []ClientSummary, since time.Time) (ClientStats, []Share, []Share) {
	var stats ClientStats
	var seconds int64
	var sessions int
	vendors, oses := map[string]int{}, map[string]int{}
	for _, c := range clients {
		if c.FirstSeenAt.Before(since) {
			stats.Returning++
		} else {
			stats.New++
		}
		if c.PrivateMAC {
			stats.Private++
		}
		seconds += c.ConnectedSeconds
		sessions += c.Sessions
		vendor := c.Vendor
		if vendor == "" {
			vendor = "Unknown"
		}
		vendors[vendor]++
		if c.Wireless {
			oses[c.OSInferred]++
		}
	}
	if sessions > 0 {
		stats.AverageSessionSeconds = seconds / int64(sessions)
	}
	return stats, shares(vendors), shares(oses)
}

func shares(m map[string]int) []Share {
	out := make([]Share, 0, len(m))
	for name, n := range m {
		out = append(out, Share{Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}
