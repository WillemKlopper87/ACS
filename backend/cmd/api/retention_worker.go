package main

import (
	"context"
	"log/slog"
	"time"

	"acs/internal/retention"
	"database/sql"
)

// retentionInterval paces the pruning pass (audit P2.3). Hourly is
// plenty: the pass is idempotent and batched, so a missed run just
// means the next one deletes a little more.
const retentionInterval = time.Hour

// runRetention prunes append-only tables per retention.PolicyFromEnv,
// starting shortly after boot (so a freshly upgraded deployment with a
// large backlog begins draining it immediately) and hourly thereafter.
func runRetention(ctx context.Context, db *sql.DB, logger *slog.Logger) {
	policy := retention.PolicyFromEnv()
	logger.Info("retention policy", "sessions_days", policy.SessionsDays, "audit_log_days", policy.AuditLogDays,
		"parameter_history_days", policy.ParameterHistoryDays, "webhook_deliveries_days", policy.WebhookDeliveriesDays,
		"finished_jobs_days", policy.FinishedJobsDays, "stale_upload_slots_days", policy.StaleUploadSlotsDays,
		"reset_tokens_days", policy.ResetTokensDays)

	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		res, err := retention.Run(ctx, db, policy)
		if err != nil {
			logger.Error("retention pass failed", "err", err)
		}
		total := int64(0)
		attrs := []any{}
		for table, n := range res {
			if n > 0 {
				attrs = append(attrs, table, n)
				total += n
			}
		}
		if total > 0 {
			logger.Info("retention pass pruned rows", attrs...)
		}
		timer.Reset(retentionInterval)
	}
}
