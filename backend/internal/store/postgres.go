// Package store owns the PostgreSQL connection and schema migrations.
// Postgres is the durable source of truth for device/session/audit state
// (design doc v3 §5.5 / §22 rule 15); Redis-backed fast dispatch queues
// are a later-phase addition once Phase 2 introduces the job queue.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed all:migrations
var migrationsFS embed.FS

// Pool sizing (audit P1.3). database/sql's defaults are unlimited open
// connections and two idle ones — unbounded against a Postgres with a
// fixed max_connections, and churny under load. These are deliberately
// modest per-process values; size ACS_DB_MAX_OPEN_CONNS across all
// replicas of all three services to fit under the server's limit.
const (
	defaultMaxOpenConns    = 20
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = 30 * time.Minute
	defaultConnMaxIdleTime = 5 * time.Minute
)

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}

// Open connects to Postgres via the pgx stdlib driver, applies pool
// limits (ACS_DB_MAX_OPEN_CONNS, ACS_DB_MAX_IDLE_CONNS,
// ACS_DB_CONN_MAX_LIFETIME, ACS_DB_CONN_MAX_IDLE_TIME), and verifies
// connectivity with a ping.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(envInt("ACS_DB_MAX_OPEN_CONNS", defaultMaxOpenConns))
	db.SetMaxIdleConns(envInt("ACS_DB_MAX_IDLE_CONNS", defaultMaxIdleConns))
	db.SetConnMaxLifetime(envDuration("ACS_DB_CONN_MAX_LIFETIME", defaultConnMaxLifetime))
	db.SetConnMaxIdleTime(envDuration("ACS_DB_CONN_MAX_IDLE_TIME", defaultConnMaxIdleTime))
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}

// migrationLockID is the pg_advisory_lock key serializing Migrate
// across replicas (audit P1.3): two instances starting together must
// not both try to apply the same file. Arbitrary but stable.
const migrationLockID = 0x4143535f4d494752 // "ACS_MIGR"

// ErrMigrationChecksum means an already-applied migration file no
// longer matches what was applied — someone edited history. Forward-
// only migrations are immutable once applied; add a new file instead.
var ErrMigrationChecksum = errors.New("applied migration has been modified")

func checksum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Migrate applies every .sql file under migrations/ that hasn't already
// been recorded in schema_migrations, in filename order (hence the
// 0001_, 0002_ prefixes). Intentionally minimal — no rollback support,
// no out-of-order application — but (audit P1.3) it is safe to run
// from several replicas at once: the whole pass runs on one dedicated
// connection holding a session-level advisory lock, so a second
// instance simply waits and then finds nothing left to apply. Each
// applied file's SHA-256 is recorded and re-verified on every start so
// an edited-after-the-fact migration is caught instead of silently
// diverging between environments.
func Migrate(ctx context.Context, db *sql.DB) error {
	// One dedicated connection: advisory locks are per session, so the
	// lock and every statement under it must share a connection.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockID)

	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT`); err != nil {
		return fmt.Errorf("add schema_migrations.checksum: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		sum := checksum(sqlBytes)

		var stored sql.NullString
		err = conn.QueryRowContext(ctx,
			`SELECT checksum FROM schema_migrations WHERE filename = $1`, name,
		).Scan(&stored)
		switch {
		case err == nil:
			// Already applied. Verify (or backfill, for rows recorded
			// before checksums existed).
			if !stored.Valid {
				if _, err := conn.ExecContext(ctx,
					`UPDATE schema_migrations SET checksum = $2 WHERE filename = $1`, name, sum); err != nil {
					return fmt.Errorf("backfill checksum for %s: %w", name, err)
				}
			} else if stored.String != sum {
				return fmt.Errorf("%w: %s (applied %s, embedded %s)", ErrMigrationChecksum, name, stored.String[:12], sum[:12])
			}
			continue
		case errors.Is(err, sql.ErrNoRows):
			// Not yet applied — fall through.
		default:
			return fmt.Errorf("check migration %s: %w", name, err)
		}

		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)`, name, sum); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}

	return nil
}
