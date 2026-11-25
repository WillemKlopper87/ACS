// Package cpemetrics holds the history of a cellular CPE's signal readings.
// This package only reads: the single writer is the recorder subpackage, so
// the database role of a service that merely serves the charts is never
// granted INSERT.
package cpemetrics

import "time"

// CreatedBy names this feature as the actor in logs and audit records.
const CreatedBy = "system:cpe-metrics"

// Sample is one reading of a CPE's radio metrics; a nil metric was not
// reported.
type Sample struct {
	RSRP, RSRQ, SNR, RSSI *float64
	Band, RAT             string
}

// Point is a stored (or bucket-averaged) reading.
type Point struct {
	At   time.Time `json:"at"`
	RSRP *float64  `json:"rsrp"`
	RSRQ *float64  `json:"rsrq"`
	SNR  *float64  `json:"snr"`
	RSSI *float64  `json:"rssi"`
	Band string    `json:"band"`
	RAT  string    `json:"rat"`
}

// History is a device's readings over a window, at most maxPoints of them.
type History struct {
	From            time.Time `json:"from"`
	To              time.Time `json:"to"`
	BucketSeconds   int64     `json:"bucket_seconds"`
	IntervalSeconds int64     `json:"interval_seconds"`
	Samples         []Point   `json:"samples"`
}
