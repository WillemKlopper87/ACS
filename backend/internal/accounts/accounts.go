// Package accounts resolves the account ID an integration or operator
// uses (the ISP's own identifier, text) to the internal key every table
// references (docs/TENANT-SCOPED-IDS-DESIGN.md).
//
// bss_accounts is the registry: one row per account, owned by a customer
// or by nobody. Tables that name an account carry both the text ID, which
// is what people and integrations see, and account_key, which is what
// rows are found by. Code never looks a row up by the text ID: it
// resolves the ID here first, so that the rule for which account an ID
// means lives in one place.
//
// Phase 1: an ID still names one account across the whole ACS.
package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// None is the key that matches no account. Resolve returns it for an ID
// with no registry row, so that a lookup by key finds nothing instead of
// every caller having to branch on "unknown account".
const None = "00000000-0000-0000-0000-000000000000"

// Queryer is the part of *sql.DB and *sql.Tx this package uses.
type Queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Resolve returns the key of the account an ID names, or None.
func Resolve(ctx context.Context, q Queryer, accountID string) (string, error) {
	var key string
	err := q.QueryRowContext(ctx, `SELECT id::text FROM bss_accounts WHERE account_id = $1`, strings.TrimSpace(accountID)).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return None, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve account: %w", err)
	}
	return key, nil
}

// Ensure returns the key of the account an ID names, registering the
// account as unowned if it is new. Use it where a row is about to be
// written for the account; use Resolve to read.
func Ensure(ctx context.Context, q Queryer, accountID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", errors.New("account_id is required")
	}
	var key string
	// The no-op update makes the statement return the row whether it was
	// inserted now, earlier, or by a concurrent transaction.
	err := q.QueryRowContext(ctx, `
		INSERT INTO bss_accounts (account_id) VALUES ($1)
		ON CONFLICT (account_id) DO UPDATE SET account_id = EXCLUDED.account_id
		RETURNING id::text`, accountID).Scan(&key)
	if err != nil {
		return "", fmt.Errorf("register account: %w", err)
	}
	return key, nil
}
