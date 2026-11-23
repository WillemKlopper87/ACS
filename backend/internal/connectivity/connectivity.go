// Package connectivity derives a device's outages from the Informs the ACS
// recorded. It is its own package, imported only by the API, so the services
// that share the devices package do not gain a read on cwmp_sessions.
package connectivity

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"acs/internal/store"
)

// Repository reads a device's Informs.
type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// An outage is a stretch in which a device that normally reports (periodic
// Informs, plus one per connection request or event) was silent. The ACS only
// ever sees the silence, so it cannot say why: the first Inform afterwards says
// whether the CPE had restarted. A restart code means power or a reboot; none
// means the CPE stayed up and its path to the ACS (WAN, radio or the ACS
// itself) was down.
const (
	OutageRestart      = "restart"      // the first Inform back carries 1 BOOT or 0 BOOTSTRAP
	OutageConnectivity = "connectivity" // it did not: the CPE stayed up
	OutageOngoing      = "ongoing"      // still silent
)

// minOutageGap is the shortest silence counted as an outage, matching the
// five minutes after which the liveness reaper marks a device OFFLINE.
const minOutageGap = 5 * time.Minute

// connectivityRowLimit bounds the sessions read for one device.
const connectivityRowLimit = 50000

// Checkin is one Inform: when it arrived and the event codes it carried.
type Checkin struct {
	At    time.Time
	Codes []string
}

// Outage is one silence. LastSeen is the Inform before it and BackAt the
// first after it (nil while ongoing), so the CPE was unreachable for at most
// BackAt-LastSeen, and for at least that minus one reporting interval.
type Outage struct {
	LastSeen time.Time  `json:"last_seen"`
	BackAt   *time.Time `json:"back_at"`
	Seconds  int64      `json:"seconds"`
	Kind     string     `json:"kind"`
}

// Connectivity is a device's reporting history over a window.
type Connectivity struct {
	From             time.Time `json:"from"`
	To               time.Time `json:"to"`
	Checkins         int       `json:"checkins"`
	IntervalSeconds  int64     `json:"interval_seconds"`  // typical time between Informs
	ThresholdSeconds int64     `json:"threshold_seconds"` // silence longer than this is an outage
	Outages          []Outage  `json:"outages"`
	// AvailabilityPercent is the share of the observed span, from the first
	// Inform in the window to To, in which the device was not in an outage.
	// Nil when there is nothing to measure yet.
	AvailabilityPercent *float64 `json:"availability_percent"`
}

func isRestart(codes []string) bool {
	for _, c := range codes {
		if c == "1 BOOT" || c == "0 BOOTSTRAP" {
			return true
		}
	}
	return false
}

// DeriveConnectivity finds the outages in a device's Informs (oldest first).
// A gap is an outage when it exceeds both five minutes and two and a half
// times the device's usual interval (the median gap), so a CPE that reports
// hourly is not flagged for a normal hour.
func DeriveConnectivity(checkins []Checkin, from, to time.Time) Connectivity {
	out := Connectivity{From: from, To: to, Checkins: len(checkins), Outages: []Outage{}}
	if len(checkins) == 0 {
		return out
	}

	gaps := make([]time.Duration, 0, len(checkins))
	for i := 1; i < len(checkins); i++ {
		gaps = append(gaps, checkins[i].At.Sub(checkins[i-1].At))
	}
	interval := time.Duration(0)
	if len(gaps) > 0 {
		sorted := append([]time.Duration(nil), gaps...)
		slices.Sort(sorted)
		interval = sorted[len(sorted)/2]
	}
	threshold := max(interval*5/2, minOutageGap)
	out.IntervalSeconds = int64(interval / time.Second)
	out.ThresholdSeconds = int64(threshold / time.Second)

	var silent time.Duration
	for i := 1; i < len(checkins); i++ {
		gap := checkins[i].At.Sub(checkins[i-1].At)
		if gap <= threshold {
			continue
		}
		back := checkins[i].At
		kind := OutageConnectivity
		if isRestart(checkins[i].Codes) {
			kind = OutageRestart
		}
		out.Outages = append(out.Outages, Outage{LastSeen: checkins[i-1].At, BackAt: &back, Seconds: int64(gap / time.Second), Kind: kind})
		silent += gap
	}
	if last := checkins[len(checkins)-1].At; to.Sub(last) > threshold {
		gap := to.Sub(last)
		out.Outages = append(out.Outages, Outage{LastSeen: last, Seconds: int64(gap / time.Second), Kind: OutageOngoing})
		silent += gap
	}
	if span := to.Sub(checkins[0].At); span > 0 {
		pct := 100 * (1 - float64(silent)/float64(span))
		if pct < 0 {
			pct = 0
		}
		out.AvailabilityPercent = &pct
	}
	return out
}

// ForDevice reads a device's Informs since `from` and derives its outages.
// The record is as long as cwmp_sessions is kept (ACS_RETENTION_SESSIONS_DAYS,
// 30 by default); a USP device does not open CWMP sessions and has none.
func (r *Repository) ForDevice(ctx context.Context, deviceID string, from, to time.Time) (Connectivity, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT opened_at, COALESCE(inform_event_codes, '{}')
		FROM cwmp_sessions
		WHERE device_id = $1 AND opened_at >= $2 AND opened_at <= $3
		ORDER BY opened_at ASC
		LIMIT $4`, deviceID, from, to, connectivityRowLimit)
	if err != nil {
		return Connectivity{}, fmt.Errorf("query device check-ins: %w", err)
	}
	defer rows.Close()

	var checkins []Checkin
	for rows.Next() {
		var c Checkin
		var codes store.StringArray
		if err := rows.Scan(&c.At, &codes); err != nil {
			return Connectivity{}, fmt.Errorf("scan device check-in: %w", err)
		}
		c.Codes = codes
		checkins = append(checkins, c)
	}
	if err := rows.Err(); err != nil {
		return Connectivity{}, err
	}
	return DeriveConnectivity(checkins, from, to), nil
}
