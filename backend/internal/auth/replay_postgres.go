package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresReplayStore is the cross-replica ReplayStore backend (audit
// P1.6): the (nonce, nc) check-and-record happens as one atomic
// INSERT ... ON CONFLICT so two cmd/acs replicas racing the same
// Authorization header can't both observe "not yet used".
type PostgresReplayStore struct {
	db *sql.DB
}

func NewPostgresReplayStore(db *sql.DB) *PostgresReplayStore {
	return &PostgresReplayStore{db: db}
}

// CheckAndRecord implements ReplayStore. A first sighting of nonce
// inserts it at ncOrdinal; a later sighting only "wins" (and is
// therefore not a replay) if ncOrdinal is strictly greater than what's
// recorded — the same invariant the in-process cache enforces, just
// visible to every replica instead of one.
func (s *PostgresReplayStore) CheckAndRecord(ctx context.Context, nonce string, ncOrdinal uint64, expires time.Time) (bool, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO cwmp_digest_nonces (nonce, last_nc, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (nonce) DO UPDATE
			SET last_nc = EXCLUDED.last_nc
			WHERE cwmp_digest_nonces.last_nc < EXCLUDED.last_nc
		RETURNING nonce`,
		nonce, int64(ncOrdinal), expires)

	var got string
	err := row.Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		// The conflicting UPDATE's WHERE clause was false — nc did not
		// advance, i.e. a genuine replay.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check digest replay: %w", err)
	}
	return true, nil
}

// Purge deletes expired nonce rows (audit P1.6) — called periodically by
// a reaper goroutine (cmd/acs/workers.go's runDigestReplayReaper) rather
// than on every check, since this table sees one write per authenticated
// CWMP request.
func (s *PostgresReplayStore) Purge(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cwmp_digest_nonces WHERE expires_at < $1`, now)
	if err != nil {
		return 0, fmt.Errorf("purge expired digest nonces: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
