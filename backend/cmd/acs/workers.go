// Background loops owned by the CWMP gateway (split out of main.go,
// audit P3.1).
package main

import (
	"acs/internal/devices"
	"acs/internal/jobs"
	"acs/internal/observability"
	"context"
	"log/slog"
	"time"
)

// devicesOnlinePollInterval is how often the acs_devices_online gauge is
// refreshed from Postgres — a periodic poll rather than an update on
// every Inform/session-close, so a Prometheus scrape never triggers an
// extra query and the hot CWMP request path stays untouched by metrics
// bookkeeping beyond simple counter increments.
const devicesOnlinePollInterval = 15 * time.Second

func pollDevicesOnlineGauge(ctx context.Context, repo *devices.Repository, metrics *observability.Metrics, logger *slog.Logger) {
	ticker := time.NewTicker(devicesOnlinePollInterval)
	defer ticker.Stop()
	for {
		counts, err := repo.CountByOnlineStatus(ctx, nil, false)
		if err != nil {
			logger.Error("failed to refresh devices-online gauge", "err", err)
		} else {
			for _, status := range []string{"ONLINE", "OFFLINE", "UNREACHABLE"} {
				metrics.DevicesOnline.WithLabelValues(status).Set(float64(counts[status]))
			}
		}
		<-ticker.C
	}
}

// leaseReaperInterval paces the stale-lease reaper (audit P1.1). Well
// under the shortest lease so a stranded job is requeued promptly; each
// pass is three indexed UPDATEs, so it is cheap at any fleet size.
const leaseReaperInterval = 30 * time.Second

func runLeaseReaper(ctx context.Context, repo *jobs.Repository, metrics *observability.Metrics, logger *slog.Logger) {
	ticker := time.NewTicker(leaseReaperInterval)
	defer ticker.Stop()
	for {
		res, err := repo.RecoverExpiredLeases(ctx)
		if err != nil {
			logger.Error("lease reaper pass failed", "err", err)
		} else {
			if res.Requeued+res.DeadLettered+res.TimedOut > 0 {
				logger.Warn("recovered stranded jobs", "requeued", res.Requeued, "dead_lettered", res.DeadLettered, "transfer_timeout", res.TimedOut)
			}
			metrics.JobsRecoveredTotal.WithLabelValues("requeued").Add(float64(res.Requeued))
			metrics.JobsRecoveredTotal.WithLabelValues("dead_lettered").Add(float64(res.DeadLettered))
			metrics.JobsRecoveredTotal.WithLabelValues("transfer_timeout").Add(float64(res.TimedOut))
		}
		if n, err := repo.CountStaleLeases(ctx); err == nil {
			metrics.JobsStaleLeases.Set(float64(n))
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// runLivenessReaper downgrades stale devices to OFFLINE and long-stale devices
// to UNREACHABLE so fleet dashboards mirror real connectedness.
func runLivenessReaper(ctx context.Context, repo *devices.Repository, logger *slog.Logger) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		if _, _, err := repo.RefreshLiveness(ctx, 5*time.Minute, 90*time.Minute); err != nil {
			logger.Error("liveness reaper pass failed", "err", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}
