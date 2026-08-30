// Package retention bounds the growth of the append-only tables (audit
// P2.3). Sessions, audit events, parameter history, webhook deliveries,
// finished jobs, abandoned upload reservations, and spent password
// reset tokens all grew without limit; nothing pruned them. This
// package deletes in bounded batches on a schedule so a long-running
// deployment's disk and query plans stay predictable. Every policy is
// a configurable number of days (0 disables that policy — for example
// where audit_log must be kept indefinitely for compliance and
// archived out-of-band instead).
package retention

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Policy holds the retention window per table in days. Zero means
// "never prune".
type Policy struct {
	SessionsDays          int
	AuditLogDays          int
	ParameterHistoryDays  int
	WebhookDeliveriesDays int
	FinishedJobsDays      int
	StaleUploadSlotsDays  int
	ResetTokensDays       int
}

// DefaultPolicy is deliberately conservative: long enough that an
// operator investigating an incident still has the trail, short enough
// that a fleet's session/history volume stays bounded. Audit events
// keep a year by default.
var DefaultPolicy = Policy{
	SessionsDays:          30,
	AuditLogDays:          365,
	ParameterHistoryDays:  90,
	WebhookDeliveriesDays: 30,
	FinishedJobsDays:      90,
	StaleUploadSlotsDays:  7,
	ResetTokensDays:       1,
}

func envDays(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

// PolicyFromEnv overlays ACS_RETENTION_*_DAYS onto DefaultPolicy.
func PolicyFromEnv() Policy {
	d := DefaultPolicy
	return Policy{
		SessionsDays:          envDays("ACS_RETENTION_SESSIONS_DAYS", d.SessionsDays),
		AuditLogDays:          envDays("ACS_RETENTION_AUDIT_LOG_DAYS", d.AuditLogDays),
		ParameterHistoryDays:  envDays("ACS_RETENTION_PARAMETER_HISTORY_DAYS", d.ParameterHistoryDays),
		WebhookDeliveriesDays: envDays("ACS_RETENTION_WEBHOOK_DELIVERIES_DAYS", d.WebhookDeliveriesDays),
		FinishedJobsDays:      envDays("ACS_RETENTION_FINISHED_JOBS_DAYS", d.FinishedJobsDays),
		StaleUploadSlotsDays:  envDays("ACS_RETENTION_STALE_UPLOAD_SLOTS_DAYS", d.StaleUploadSlotsDays),
		ResetTokensDays:       envDays("ACS_RETENTION_RESET_TOKENS_DAYS", d.ResetTokensDays),
	}
}

// batchSize bounds each DELETE so a first prune on a large backlog
// never takes a long lock or bloats one transaction; Run loops until a
// batch comes back short.
const batchSize = 5000

// rule is one prunable table: the DELETE is expressed as "rows whose
// id is in the oldest N matching" so every table shares the batching
// shape regardless of its own predicate.
type rule struct {
	name      string
	days      func(Policy) int
	predicate string // WHERE clause fragment using $1 as the cutoff timestamp
	idColumn  string
}

var rules = []rule{
	{"cwmp_sessions", func(p Policy) int { return p.SessionsDays },
		"closed_at IS NOT NULL AND closed_at < $1", "id"},
	{"audit_log", func(p Policy) int { return p.AuditLogDays },
		"occurred_at < $1", "id"},
	{"parameter_history", func(p Policy) int { return p.ParameterHistoryDays },
		"recorded_at < $1", "id"},
	{"webhook_deliveries", func(p Policy) int { return p.WebhookDeliveriesDays },
		"status <> 'PENDING' AND created_at < $1", "id"},
	{"jobs", func(p Policy) int { return p.FinishedJobsDays },
		"status IN ('SUCCESS', 'FAILED', 'TIMEOUT') AND completed_at IS NOT NULL AND completed_at < $1", "id"},
	{"uploaded_files", func(p Policy) int { return p.StaleUploadSlotsDays },
		"status = 'PENDING' AND created_at < $1", "id"},
	{"password_reset_tokens", func(p Policy) int { return p.ResetTokensDays },
		"(used_at IS NOT NULL OR expires_at < now()) AND expires_at < $1", "id"},
}

// Result is per-table rows deleted in one Run.
type Result map[string]int64

// Run applies every enabled policy once, batching deletes. It returns
// the rows removed per table; an error on one table does not stop the
// others (the error reported is the first one seen).
func Run(ctx context.Context, db *sql.DB, p Policy) (Result, error) {
	res := Result{}
	var firstErr error
	for _, r := range rules {
		days := r.days(p)
		if days <= 0 {
			continue
		}
		cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
		query := fmt.Sprintf(`DELETE FROM %s WHERE %s IN (SELECT %s FROM %s WHERE %s LIMIT %d)`,
			r.name, r.idColumn, r.idColumn, r.name, r.predicate, batchSize)
		for {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			out, err := db.ExecContext(ctx, query, cutoff)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("prune %s: %w", r.name, err)
				}
				break
			}
			n, _ := out.RowsAffected()
			res[r.name] += n
			if n < batchSize {
				break
			}
		}
	}
	return res, firstErr
}
