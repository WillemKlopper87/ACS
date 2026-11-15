// Package accounts resolves the account ID an integration or operator
// uses (the ISP's own identifier, text) to the internal key every table
// references (docs/TENANT-SCOPED-IDS-DESIGN.md).
//
// bss_accounts is the registry: one row per account, owned by a customer
// or by nobody. An ID is unique within a customer, and within the unowned
// accounts; two customers can use the same ID. Tables that name an
// account carry both the text ID, which is what people and integrations
// see, and account_key, which is what rows are found by. Code never looks
// a row up by the text ID: it resolves the ID here first, so that the
// rule for which account an ID means lives in one place.
//
// Which account an ID means depends on who is asking, and that travels
// in the context:
//
//   - WithCustomer: the caller acts for one customer (an integration
//     bound to an ISP, an operator who named the customer, a worker
//     handling a row of that customer). IDs resolve inside that
//     customer's accounts only. The empty customer is the unowned
//     accounts.
//   - ForDevice: the ID came from a device's mapping (telemetry, alert
//     incidents). It resolves to the account of that name the device is
//     mapped to.
//   - Neither: the ID must be unambiguous. If more than one customer has
//     an account of that name the answer is ErrAmbiguous, never a guess.
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

// ErrAmbiguous: the ID names accounts of more than one customer and the
// caller did not say which customer it means.
var ErrAmbiguous = errors.New("account_id is used by more than one customer; say which customer is meant")

// Queryer is the part of *sql.DB and *sql.Tx this package uses.
type Queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type ctxKey int

const (
	customerKey ctxKey = iota
	deviceKey
)

// WithCustomer makes IDs resolve inside one customer's accounts. An empty
// customerID means the unowned accounts.
func WithCustomer(ctx context.Context, customerID string) context.Context {
	return context.WithValue(ctx, customerKey, strings.TrimSpace(customerID))
}

// Customer returns the customer IDs resolve in, and whether one was set.
func Customer(ctx context.Context) (string, bool) {
	c, ok := ctx.Value(customerKey).(string)
	return c, ok
}

// ForDevice makes an ID resolve to the account of that name the device is
// mapped to, when no customer is set. A device belongs to one customer,
// so its mappings name the account without ambiguity.
func ForDevice(ctx context.Context, deviceID string) context.Context {
	if strings.TrimSpace(deviceID) == "" {
		return ctx
	}
	return context.WithValue(ctx, deviceKey, deviceID)
}

// Resolve returns the key of the account an ID names for this caller, or
// None.
func Resolve(ctx context.Context, q Queryer, accountID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if customer, ok := Customer(ctx); ok {
		return one(q.QueryRowContext(ctx,
			`SELECT id::text FROM bss_accounts WHERE account_id = $1 AND COALESCE(customer_id::text, '') = $2`, accountID, customer))
	}
	if device, ok := ctx.Value(deviceKey).(string); ok {
		key, err := one(q.QueryRowContext(ctx, `
			SELECT account_key::text FROM account_device_mappings
			 WHERE device_id::text = $2 AND account_id = $1
			 ORDER BY (unassigned_at IS NULL) DESC, assigned_at DESC LIMIT 1`, accountID, device))
		if err != nil || key != None {
			return key, err
		}
	}
	// Nobody said which customer: the ID has to speak for itself.
	var key string
	var n int
	err := q.QueryRowContext(ctx, `SELECT COALESCE(min(id::text), ''), count(*) FROM bss_accounts WHERE account_id = $1`, accountID).Scan(&key, &n)
	switch {
	case err != nil:
		return "", fmt.Errorf("resolve account: %w", err)
	case n == 0:
		return None, nil
	case n > 1:
		return "", fmt.Errorf("%w: %s", ErrAmbiguous, accountID)
	}
	return key, nil
}

func one(row *sql.Row) (string, error) {
	var key string
	err := row.Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return None, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve account: %w", err)
	}
	return key, nil
}

// Ensure returns the key of the account an ID names for this caller,
// registering the account if it is new: in the caller's customer when one
// is set, otherwise as unowned. Use it where a row is about to be written
// for the account; use Resolve to read.
func Ensure(ctx context.Context, q Queryer, accountID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", errors.New("account_id is required")
	}
	key, err := Resolve(ctx, q, accountID)
	if err != nil || key != None {
		return key, err
	}
	customer, _ := Customer(ctx)
	// The no-op update makes the statement return the row whether it was
	// inserted now or by a concurrent transaction.
	err = q.QueryRowContext(ctx, `
		INSERT INTO bss_accounts (account_id, customer_id) VALUES ($1, NULLIF($2, '')::uuid)
		ON CONFLICT ((COALESCE(customer_id::text, '')), account_id) DO UPDATE SET account_id = EXCLUDED.account_id
		RETURNING id::text`, accountID, customer).Scan(&key)
	if err != nil {
		return "", fmt.Errorf("register account: %w", err)
	}
	return key, nil
}

// ScopeFilter returns the customer an identifier that is not an account
// ID (an order ID, an event's source key) must be looked up in, and
// whether to filter at all. Callers add
//
//	($n = false OR <owner expression> = $m)
//
// to their query with the two values.
func ScopeFilter(ctx context.Context) (filter bool, customer string) {
	customer, ok := Customer(ctx)
	return ok, customer
}

// ScopeToAccount settles, for a caller that did not say which customer it
// acts for, which customer the rest of its request is about: the owner of
// the one account the ID names. Other identifiers in the same request (an
// order ID) are then looked up in that customer too. It returns
// ErrAmbiguous when the ID alone does not name one account. A context
// that already has a customer, and an empty ID, are returned unchanged.
func ScopeToAccount(ctx context.Context, q Queryer, accountID string) (context.Context, error) {
	if _, ok := Customer(ctx); ok || strings.TrimSpace(accountID) == "" {
		return ctx, nil
	}
	key, err := Resolve(ctx, q, accountID)
	if err != nil {
		return ctx, err
	}
	owner := ""
	if key != None {
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(customer_id::text, '') FROM bss_accounts WHERE id = $1`, key).Scan(&owner); err != nil {
			return ctx, fmt.Errorf("resolve account owner: %w", err)
		}
	}
	// No account of that ID anywhere: a write creates it unowned.
	return WithCustomer(ctx, owner), nil
}
