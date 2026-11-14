package bss

import (
	"acs/internal/accounts"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Account ownership (migration 0089): a BSS account belongs to at most one
// ACS customer (an ISP). OAuth clients bound to a customer act only on its
// accounts and claim a new one on their first write. Clients that are not
// bound (global or account-listed, the legacy model) are unaffected.

// ErrAccountHasHistory: the account was used before customer binding (by
// a global client), so no bound client may claim it; an operator assigns
// its owner instead (AssignAccountOwner).
var ErrAccountHasHistory = errors.New("account has data from before customer binding; an operator must assign its customer")

// AccountOwner returns the customer that owns an account, "" if none.
func (r *Repository) AccountOwner(ctx context.Context, accountID string) (string, error) {
	accountKey, err := accounts.Resolve(ctx, r.db, accountID)
	if err != nil {
		return "", err
	}
	var owner string
	err = r.db.QueryRowContext(ctx, `SELECT COALESCE(customer_id::text, '') FROM bss_accounts WHERE id = $1`, accountKey).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up account owner: %w", err)
	}
	return owner, nil
}

// ErrAccountIDTaken: the customer an account is being assigned to already
// has an account with that ID.
var ErrAccountIDTaken = errors.New("that customer already has an account with this account_id")

// ClaimAccount registers an account for a customer on its first write and
// returns the account's owner afterwards. The account is created in the
// customer's own namespace, so no other customer's use of the same ID
// can stand in the way.
//
// One thing still can: an unowned account of that ID that already has
// mappings, orders, service orders or webhook subscriptions (written from
// the console or by a global client). That is very likely this
// customer's account from before it was bound, and starting a second,
// empty one beside it would orphan the history, so the claim is refused
// (ErrAccountHasHistory) until an operator assigns the account
// (AssignAccountOwner).
func (r *Repository) ClaimAccount(ctx context.Context, accountID, customerID, claimedBy string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO bss_accounts (account_id, customer_id, claimed_by)
		SELECT $1, $2::uuid, $3
		 WHERE NOT EXISTS (
			SELECT 1 FROM bss_accounts a
			 WHERE a.account_id = $1 AND a.customer_id IS NULL
			   AND (EXISTS (SELECT 1 FROM account_device_mappings WHERE account_key = a.id)
			     OR EXISTS (SELECT 1 FROM bss_orders WHERE account_key = a.id)
			     OR EXISTS (SELECT 1 FROM service_orders WHERE account_key = a.id)
			     OR EXISTS (SELECT 1 FROM webhook_subscriptions WHERE account_key = a.id)))
		ON CONFLICT ((COALESCE(customer_id::text, '')), account_id) DO NOTHING`, accountID, customerID, claimedBy)
	if err != nil {
		return "", fmt.Errorf("claim account: %w", err)
	}
	owner, err := r.AccountOwner(accounts.WithCustomer(ctx, customerID), accountID)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 && owner == "" {
		return "", ErrAccountHasHistory
	}
	return owner, nil
}

// AssignAccountOwner sets an account's customer as an operator decision,
// including for accounts with history. Moving an owned account to another
// customer is allowed here and only here. The account is the one the
// caller's context names (accounts.WithCustomer when the ID is used by
// more than one customer); an ID nobody uses yet is registered for the
// customer. A move onto an ID the customer already has is refused:
// ErrAccountIDTaken.
func (r *Repository) AssignAccountOwner(ctx context.Context, accountID, customerID, by string) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return errors.New("account_id is required")
	}
	accountKey, err := accounts.Resolve(ctx, r.db, accountID)
	if err != nil {
		return err
	}
	if accountKey == accounts.None {
		_, err = r.db.ExecContext(ctx, `
			INSERT INTO bss_accounts (account_id, customer_id, claimed_by) VALUES ($1, $2::uuid, $3)
			ON CONFLICT ((COALESCE(customer_id::text, '')), account_id) DO NOTHING`, accountID, customerID, by)
		if err != nil {
			return fmt.Errorf("assign account owner: %w", err)
		}
		return nil
	}
	// The orders' scope is the one copy of the owner outside the registry
	// (it backs the per-customer uniqueness of order IDs), so it moves in
	// the same statement.
	_, err = r.db.ExecContext(ctx, `
		WITH acct AS (
			UPDATE bss_accounts SET customer_id = $2::uuid, claimed_by = $3 WHERE id = $1::uuid
			RETURNING id, COALESCE(customer_id::text, '') AS scope
		), orders AS (
			UPDATE bss_orders o SET order_scope = acct.scope FROM acct WHERE o.account_key = acct.id
		)
		UPDATE service_orders o SET order_scope = acct.scope FROM acct WHERE o.account_key = acct.id`,
		accountKey, customerID, by)
	if isUniqueViolation(err) {
		return ErrAccountIDTaken
	}
	if err != nil {
		return fmt.Errorf("assign account owner: %w", err)
	}
	return nil
}

// OwnedAccounts lists a customer's accounts.
func (r *Repository) OwnedAccounts(ctx context.Context, customerID string) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT account_id FROM bss_accounts WHERE customer_id = $1::uuid`, customerID)
	if err != nil {
		return nil, fmt.Errorf("list owned accounts: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// deviceForAccount resolves an oui_serial for a mapping write and applies
// account ownership to the device: a device of another customer is not
// found (no existence oracle across ISPs), and a device with no customer
// joins the account's customer, unless requireAllocated is set, in which
// case it is not found either: only a device already allocated to the
// account's customer can be taken (Repository.RequireAllocatedDevices).
// Accounts without an owner keep the legacy behaviour.
func deviceForAccount(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, ouiSerial, accountKey string, requireAllocated bool) (string, error) {
	var deviceID, deviceCustomer, owner string
	err := q.QueryRowContext(ctx, `
		SELECT d.id::text, COALESCE(d.customer_id::text, ''),
		       COALESCE((SELECT customer_id::text FROM bss_accounts WHERE id = $2::uuid), '')
		  FROM devices d WHERE d.oui_serial = $1`, ouiSerial, accountKey).Scan(&deviceID, &deviceCustomer, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrDeviceNotFound, ouiSerial)
	}
	if err != nil {
		return "", fmt.Errorf("resolve device: %w", err)
	}
	switch {
	case owner == "":
	case deviceCustomer == "" && requireAllocated:
		// The same answer as for another customer's device: a caller must
		// not learn which serials exist but are unallocated.
		return "", fmt.Errorf("%w: %s", ErrDeviceNotFound, ouiSerial)
	case deviceCustomer == "":
		if _, err := q.ExecContext(ctx, `UPDATE devices SET customer_id = $2::uuid, last_updated_at = now() WHERE id = $1 AND customer_id IS NULL`,
			deviceID, owner); err != nil {
			return "", fmt.Errorf("assign device to the account's customer: %w", err)
		}
	case deviceCustomer != owner:
		return "", fmt.Errorf("%w: %s", ErrDeviceNotFound, ouiSerial)
	}
	return deviceID, nil
}

// accountFilter resolves an optional account filter to a key: "" (no
// filter) stays "", and an ID with no account becomes accounts.None,
// which matches nothing.
func (r *Repository) accountFilter(ctx context.Context, accountID string) (string, error) {
	if strings.TrimSpace(accountID) == "" {
		return "", nil
	}
	return accounts.Resolve(ctx, r.db, accountID)
}

// accountKeyForWrite is accountFilter for a row about to be written: the
// account is registered if it is new. "" stays "" for rows that name no
// account (an event of an unmapped device). deviceID, when the row has
// one, says which account of that name is meant if the caller's context
// does not (accounts.ForDevice).
func (r *Repository) accountKeyForWrite(ctx context.Context, accountID, deviceID string) (string, error) {
	if strings.TrimSpace(accountID) == "" {
		return "", nil
	}
	return accounts.Ensure(accounts.ForDevice(ctx, deviceID), r.db, accountID)
}

// Rows found by their own ID (a mapping, an order, a subscription) are
// found inside the caller's customer too, when it acts for one: an ID
// from another customer's row then finds nothing, whatever account ID the
// caller goes on to present. Checking the row's account ID afterwards is
// not enough, because two customers can use the same account ID.
//
// scopeArgs are the two query arguments of a "find by a key the caller
// chose" predicate, ($n = false OR <owner> = $m): whether the caller acts
// for one customer, and which.
func scopeArgs(ctx context.Context) (bool, string) {
	return accounts.ScopeFilter(ctx)
}

// alarmOwner and problemOwner are the <owner> of that predicate for rows
// that reach their customer through the account.
const (
	mappingOwner      = `COALESCE((SELECT sa.customer_id::text FROM bss_accounts sa WHERE sa.id = account_device_mappings.account_key), '')`
	subscriptionOwner = `COALESCE((SELECT sa.customer_id::text FROM bss_accounts sa WHERE sa.id = webhook_subscriptions.account_key), '')`
	alarmOwner        = `COALESCE((SELECT sa.customer_id::text FROM bss_accounts sa WHERE sa.id = tmf_alarms.account_key), '')`
	problemOwner      = `COALESCE((SELECT sa.customer_id::text FROM bss_accounts sa WHERE sa.id = tmf_service_problems.account_key), '')`
)

// ScopeToAccount is accounts.ScopeToAccount on this repository's database.
func (r *Repository) ScopeToAccount(ctx context.Context, accountID string) (context.Context, error) {
	return accounts.ScopeToAccount(ctx, r.db, accountID)
}

// AccountKey returns the registry key of the account an ID names for the
// caller (accounts.Resolve), accounts.None if there is none.
func (r *Repository) AccountKey(ctx context.Context, accountID string) (string, error) {
	return accounts.Resolve(ctx, r.db, accountID)
}
