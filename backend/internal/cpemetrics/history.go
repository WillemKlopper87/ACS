package cpemetrics

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// maxRows caps how many raw rows one History call reads.
const maxRows = 50000

// Repository reads the stored readings.
type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// LastSampleAt returns the device's most recent sample time (zero if none).
func (r *Repository) LastSampleAt(ctx context.Context, deviceID string) (time.Time, error) {
	var at sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT MAX(sampled_at) FROM device_cpe_samples WHERE device_id=$1`, deviceID).Scan(&at)
	if err != nil {
		return time.Time{}, err
	}
	return at.Time, nil
}

// History returns the device's readings in [from, to], oldest first. When
// there are more than maxPoints rows they are averaged into buckets of
// BucketSeconds so the result has at most maxPoints points.
func (r *Repository) History(ctx context.Context, deviceID string, from, to time.Time, maxPoints int) (History, error) {
	out := History{From: from, To: to, Samples: []Point{}}
	rows, err := r.db.QueryContext(ctx, `
		SELECT sampled_at, rsrp, rsrq, snr, rssi, band, rat
		FROM device_cpe_samples
		WHERE device_id = $1 AND sampled_at >= $2 AND sampled_at <= $3
		ORDER BY sampled_at
		LIMIT $4`, deviceID, from, to, maxRows)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var raw []Point
	for rows.Next() {
		var p Point
		var a, b, c, d sql.NullFloat64
		if err := rows.Scan(&p.At, &a, &b, &c, &d, &p.Band, &p.RAT); err != nil {
			return out, err
		}
		p.At = p.At.UTC()
		p.RSRP, p.RSRQ, p.SNR, p.RSSI = nullable(a), nullable(b), nullable(c), nullable(d)
		raw = append(raw, p)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.IntervalSeconds = medianGapSeconds(raw)
	if maxPoints <= 0 || len(raw) <= maxPoints {
		if raw != nil {
			out.Samples = raw
		}
		return out, nil
	}
	span := to.Sub(from)
	bucket := (int64(span/time.Second) + int64(maxPoints) - 1) / int64(maxPoints)
	if bucket < 1 {
		bucket = 1
	}
	out.BucketSeconds = bucket
	out.Samples = bucketize(raw, from, bucket)
	return out, nil
}

func nullable(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

func medianGapSeconds(pts []Point) int64 {
	if len(pts) < 2 {
		return 0
	}
	gaps := make([]int64, 0, len(pts)-1)
	for i := 1; i < len(pts); i++ {
		gaps = append(gaps, int64(pts[i].At.Sub(pts[i-1].At)/time.Second))
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	return gaps[len(gaps)/2]
}

type acc struct {
	sum float64
	n   int
}

func (a *acc) add(v *float64) {
	if v != nil {
		a.sum += *v
		a.n++
	}
}

func (a *acc) mean() *float64 {
	if a.n == 0 {
		return nil
	}
	m := a.sum / float64(a.n)
	return &m
}

// bucketize averages the (time-ordered) points into buckets bucketSeconds
// wide, counted from `from`. A bucket's At is its start; its Band and RAT
// are the last non-empty values in it.
func bucketize(raw []Point, from time.Time, bucketSeconds int64) []Point {
	var out []Point
	cur := int64(-1)
	var rsrp, rsrq, snr, rssi acc
	var p Point
	flush := func() {
		if cur < 0 {
			return
		}
		p.RSRP, p.RSRQ, p.SNR, p.RSSI = rsrp.mean(), rsrq.mean(), snr.mean(), rssi.mean()
		out = append(out, p)
	}
	for _, x := range raw {
		idx := int64(x.At.Sub(from)/time.Second) / bucketSeconds
		if idx < 0 {
			idx = 0
		}
		if idx != cur {
			flush()
			cur = idx
			rsrp, rsrq, snr, rssi = acc{}, acc{}, acc{}, acc{}
			p = Point{At: from.Add(time.Duration(idx*bucketSeconds) * time.Second).UTC()}
		}
		rsrp.add(x.RSRP)
		rsrq.add(x.RSRQ)
		snr.add(x.SNR)
		rssi.add(x.RSSI)
		if x.Band != "" {
			p.Band = x.Band
		}
		if x.RAT != "" {
			p.RAT = x.RAT
		}
	}
	flush()
	return out
}
