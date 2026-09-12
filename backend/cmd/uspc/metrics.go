package main

import (
	"github.com/prometheus/client_golang/prometheus"

	"acs/internal/observability"
	"acs/internal/usp/mtp"
)

// uspMetrics are cmd/uspc's own metrics, registered on the shared
// observability.Metrics registry via its Factory() accessor rather
// than living inside internal/observability itself -- these two series
// (connections and records) are specific to this service, not part of
// the fixed set every service shares. Building them via Factory()
// rather than a bare promauto.With(m.Registry()) gives them the same
// "service" const label every other acs_* metric in the registry
// carries.
type uspMetrics struct {
	// connections is the count of live agent connections, by MTP.
	connections *prometheus.GaugeVec
	// records is every record processed, by MTP, direction (in/out) and
	// result (ok/decode_error/no_payload/unsupported).
	records *prometheus.CounterVec
}

func newUSPMetrics(m *observability.Metrics) *uspMetrics {
	factory := m.Factory()
	um := &uspMetrics{
		connections: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "acs_usp_connections",
			Help: "Live USP agent connections, by MTP.",
		}, []string{"mtp"}),
		records: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "acs_usp_records_total",
			Help: "USP records processed, by MTP, direction, and result.",
		}, []string{"mtp", "direction", "result"}),
	}

	// A CounterVec/GaugeVec exposes nothing on /metrics until a label
	// combination is actually observed -- initialize the two known MTPs
	// at zero so both series (and their HELP/TYPE lines) exist from
	// startup, for a dashboard or an alert rule with no live agents yet.
	// records gets one zero entry per mtp/direction with result "ok" --
	// enough to seed the series without trying to enumerate every
	// direction/result combination.
	for _, kind := range []string{string(mtp.KindWebSocket), string(mtp.KindMQTT)} {
		um.connections.WithLabelValues(kind)
		for _, direction := range []string{"in", "out"} {
			um.records.WithLabelValues(kind, direction, "ok")
		}
	}

	return um
}
