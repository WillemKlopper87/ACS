package bss

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// IdempotencyTTL is how long a completed Idempotency-Key answers a retry.
// After it the key is free again.
const IdempotencyTTL = 24 * time.Hour

var (
	// ErrIdempotencyMismatch: the key was used for a different request.
	ErrIdempotencyMismatch = errors.New("idempotency key used for a different request")
	// ErrIdempotencyInFlight: the first request with the key has not
	// finished yet.
	ErrIdempotencyInFlight = errors.New("idempotency key in flight")
)

// IdempotentResponse is a stored answer to replay.
type IdempotentResponse struct {
	Status   int
	Body     json.RawMessage
	Location string
}

// BeginIdempotent claims key for a request. It returns nil when the caller
// should run the request (and then CompleteIdempotent or
// ReleaseIdempotent), the stored response when an earlier identical
// request finished, ErrIdempotencyInFlight while it runs, and
// ErrIdempotencyMismatch for another request under the same key.
func (r *Repository) BeginIdempotent(ctx context.Context, accountID, key, operation, requestHash string) (*IdempotentResponse, error) {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM bss_idempotency_keys WHERE account_id = $1 AND key = $2 AND created_at < now() - make_interval(secs => $3::float8)`,
		accountID, key, IdempotencyTTL.Seconds()); err != nil {
		return nil, fmt.Errorf("expire idempotency key: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO bss_idempotency_keys (account_id, key, operation, request_hash) VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id, key) DO NOTHING`, accountID, key, operation, requestHash)
	if err != nil {
		return nil, fmt.Errorf("claim idempotency key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil, nil
	}
	var (
		op, hash string
		status   sql.NullInt64
		body     []byte
		location sql.NullString
	)
	err = r.db.QueryRowContext(ctx, `SELECT operation, request_hash, status, response, location FROM bss_idempotency_keys WHERE account_id = $1 AND key = $2`,
		accountID, key).Scan(&op, &hash, &status, &body, &location)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrIdempotencyInFlight // released between the insert and the read; retry
	}
	if err != nil {
		return nil, fmt.Errorf("read idempotency key: %w", err)
	}
	if op != operation || hash != requestHash {
		return nil, ErrIdempotencyMismatch
	}
	if !status.Valid {
		return nil, ErrIdempotencyInFlight
	}
	return &IdempotentResponse{Status: int(status.Int64), Body: body, Location: location.String}, nil
}

// CompleteIdempotent stores the response a claimed key answers retries with.
func (r *Repository) CompleteIdempotent(ctx context.Context, accountID, key string, resp IdempotentResponse) error {
	_, err := r.db.ExecContext(ctx, `UPDATE bss_idempotency_keys SET status = $3, response = $4, location = NULLIF($5, ''), completed_at = now()
		WHERE account_id = $1 AND key = $2`, accountID, key, resp.Status, []byte(resp.Body), resp.Location)
	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	return nil
}

// ReleaseIdempotent frees a claimed key without a stored answer (the
// request failed on the server's side), so a retry runs again.
func (r *Repository) ReleaseIdempotent(ctx context.Context, accountID, key string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM bss_idempotency_keys WHERE account_id = $1 AND key = $2 AND status IS NULL`, accountID, key)
	return err
}
