// Metrics support (build plan §4 Phase 7 / design doc v3 Phase 7:
// "Dashboards, Alerting"). Each of the three services (cmd/acs, cmd/api,
// cmd/bssadapter) creates its own Metrics with its own registry rather
// than sharing prometheus's global DefaultRegisterer — costs nothing and
// avoids that package-level mutable state, and a "service" const label
// keeps every metric attributable if these were ever scraped through one
// shared Prometheus job.
package observability

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is a per-process Prometheus registry plus the counters/gauges
// this codebase's three services actually populate. Not every field is
// used by every service — cmd/acs never sees HTTP route metrics (its one
// endpoint is CWMP, not a REST fan-out), cmd/bssadapter never sees
// DevicesOnline. Handlers just don't call the ones that don't apply.
type Metrics struct {
	registry *prometheus.Registry

	HTTPRequestsTotal   *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec

	JobsCreatedTotal   *prometheus.CounterVec
	JobsCompletedTotal *prometheus.CounterVec
	JobsRecoveredTotal *prometheus.CounterVec // audit P1.1: stale-lease reaper outcomes
	JobsStaleLeases    prometheus.Gauge

	SessionsOpenedTotal prometheus.Counter
	InformsTotal        prometheus.Counter

	RateLimitRejectedTotal prometheus.Counter

	DevicesOnline *prometheus.GaugeVec

	factory promauto.Factory
	labels  prometheus.Labels
}

// ObserveDB exposes database/sql pool statistics (audit P1.3): open,
// in-use, and idle connections plus the cumulative count of goroutines
// that had to wait for one — the signal that the pool is undersized
// (or Postgres is saturated) before latency graphs show it.
func (m *Metrics) ObserveDB(db *sql.DB) {
	gauge := func(name, help string, f func(sql.DBStats) float64) {
		m.factory.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help, ConstLabels: m.labels},
			func() float64 { return f(db.Stats()) })
	}
	gauge("acs_db_open_connections", "Open connections in the database/sql pool.", func(s sql.DBStats) float64 { return float64(s.OpenConnections) })
	gauge("acs_db_in_use_connections", "Pool connections currently in use.", func(s sql.DBStats) float64 { return float64(s.InUse) })
	gauge("acs_db_idle_connections", "Pool connections currently idle.", func(s sql.DBStats) float64 { return float64(s.Idle) })
	gauge("acs_db_wait_count_total", "Cumulative number of times a caller waited for a pool connection.", func(s sql.DBStats) float64 { return float64(s.WaitCount) })
	gauge("acs_db_wait_duration_seconds_total", "Cumulative time callers spent waiting for a pool connection.", func(s sql.DBStats) float64 { return s.WaitDuration.Seconds() })
}

// NewMetrics builds a fresh registry for one process. service becomes a
// constant "service" label on every metric it registers.
func NewMetrics(service string) *Metrics {
	reg := prometheus.NewRegistry()
	factory := promauto.With(reg)
	constLabels := prometheus.Labels{"service": service}

	return &Metrics{
		registry: reg,
		factory:  factory,
		labels:   constLabels,
		HTTPRequestsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name:        "acs_http_requests_total",
			Help:        "Total HTTP requests handled, by method, route, and status class.",
			ConstLabels: constLabels,
		}, []string{"method", "route", "status"}),
		HTTPRequestDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:        "acs_http_request_duration_seconds",
			Help:        "HTTP request duration in seconds, by method and route.",
			ConstLabels: constLabels,
			Buckets:     prometheus.DefBuckets,
		}, []string{"method", "route"}),
		JobsCreatedTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name:        "acs_jobs_created_total",
			Help:        "Jobs created, by type.",
			ConstLabels: constLabels,
		}, []string{"type"}),
		JobsCompletedTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name:        "acs_jobs_completed_total",
			Help:        "Jobs completed, by type and terminal status.",
			ConstLabels: constLabels,
		}, []string{"type", "status"}),
		JobsRecoveredTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name:        "acs_jobs_recovered_total",
			Help:        "Jobs recovered by the stale-lease reaper, by outcome (requeued, dead_lettered, transfer_timeout).",
			ConstLabels: constLabels,
		}, []string{"outcome"}),
		JobsStaleLeases: factory.NewGauge(prometheus.GaugeOpts{
			Name:        "acs_jobs_stale_leases",
			Help:        "Leased jobs currently past their lease deadline (should stay near zero when the reaper is healthy).",
			ConstLabels: constLabels,
		}),
		SessionsOpenedTotal: factory.NewCounter(prometheus.CounterOpts{
			Name:        "acs_cwmp_sessions_opened_total",
			Help:        "CWMP sessions opened.",
			ConstLabels: constLabels,
		}),
		InformsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name:        "acs_cwmp_informs_total",
			Help:        "Inform RPCs received.",
			ConstLabels: constLabels,
		}),
		RateLimitRejectedTotal: factory.NewCounter(prometheus.CounterOpts{
			Name:        "acs_rate_limit_rejected_total",
			Help:        "Requests rejected by a rate limiter.",
			ConstLabels: constLabels,
		}),
		DevicesOnline: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name:        "acs_devices_online",
			Help:        "Devices currently in each online_status.",
			ConstLabels: constLabels,
		}, []string{"online_status"}),
	}
}

// Handler serves this process's metrics in the Prometheus exposition
// format. Deliberately unauthenticated wherever it's mounted (a scraper
// has no operator JWT or CPE credential to present) — the same "public,
// not an operator route" treatment as cmd/api's firmware file-serve
// endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// InstrumentHTTP wraps a handler to record request count and duration.
// route must be a low-cardinality label — the mux pattern ("GET
// /api/v1/devices/{id}"), never the raw URL, which would blow up label
// cardinality with one series per device ID.
func (m *Metrics) InstrumentHTTP(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)

		statusClass := strconv.Itoa(rec.status/100) + "xx"
		m.HTTPRequestsTotal.WithLabelValues(r.Method, route, statusClass).Inc()
		m.HTTPRequestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
