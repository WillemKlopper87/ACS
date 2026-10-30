package bss

import (
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
	var owner string
	err := r.db.QueryRowContext(ctx, `SELECT customer_id::text FROM bss_accounts WHERE account_id = $1`, strings.TrimSpace(accountID)).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up account owner: %w", err)
	}
	return owner, nil
}

// ClaimAccount gives an unowned, unused account to a customer and returns
// the account's owner afterwards, which is another customer when that one
// won a race. An account with mappings, orders, service orders or webhook
// subscriptions from before binding is never claimed: ErrAccountHasHistory.
func (r *Repository) ClaimAccount(ctx context.Context, accountID, customerID, claimedBy string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO bss_accounts (account_id, customer_id, claimed_by)
		SELECT $1, $2::uuid, $3
		 WHERE NOT EXISTS (SELECT 1 FROM account_device_mappings WHERE account_id = $1)
		   AND NOT EXISTS (SELECT 1 FROM bss_orders WHERE account_id = $1)
		   AND NOT EXISTS (SELECT 1 FROM service_orders WHERE account_id = $1)
		   AND NOT EXISTS (SELECT 1 FROM webhook_subscriptions WHERE account_id = $1)
		ON CONFLICT (account_id) DO NOTHING`, accountID, customerID, claimedBy)
	if err != nil {
		return "", fmt.Errorf("claim account: %w", err)
	}
	owner, err := r.AccountOwner(ctx, accountID)
	if err != nil {
		return "", err
	}
	if owner == "" {
		return "", ErrAccountHasHistory
	}
	return owner, nil
}

// AssignAccountOwner sets an account's customer as an operator decision,
// including for accounts with history. Moving an owned account to another
// customer is allowed here and only here.
func (r *Repository) AssignAccountOwner(ctx context.Context, accountID, customerID, by string) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return errors.New("account_id is required")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO bss_accounts (account_id, customer_id, claimed_by) VALUES ($1, $2::uuid, $3)
		ON CONFLICT (account_id) DO UPDATE SET customer_id = EXCLUDED.customer_id, claimed_by = EXCLUDED.claimed_by`,
		accountID, customerID, by)
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
// joins the account's customer. Accounts without an owner keep the legacy
// behaviour.
func deviceForAccount(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, ouiSerial, accountID string) (string, error) {
	var deviceID, deviceCustomer, owner string
	err := q.QueryRowContext(ctx, `
		SELECT d.id::text, COALESCE(d.customer_id::text, ''),
		       COALESCE((SELECT customer_id::text FROM bss_accounts WHERE account_id = $2), '')
		  FROM devices d WHERE d.oui_serial = $1`, ouiSerial, accountID).Scan(&deviceID, &deviceCustomer, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrDeviceNotFound, ouiSerial)
	}
	if err != nil {
		return "", fmt.Errorf("resolve device: %w", err)
	}
	switch {
	case owner == "":
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
