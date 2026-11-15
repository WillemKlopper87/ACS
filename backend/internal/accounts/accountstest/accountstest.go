// Package accountstest helps test fixtures that write account-keyed rows
// directly, bypassing the repositories.
package accountstest

// EnsureCTE returns the opening of a statement that makes sure an account
// of the ID named by the SQL expression accountExpr (a literal or a
// parameter) exists, and exposes its key as (SELECT id FROM acct). Put it
// in front of an INSERT that needs account_key:
//
//	accountstest.EnsureCTE("$1") + `INSERT INTO account_device_mappings
//	    (id, account_id, account_key, ...) VALUES ($2, $1, (SELECT id FROM acct), ...)`
//
// An account of that ID that already exists is used, whoever owns it (the
// oldest, if a test has made several); otherwise one is registered as
// unowned.
func EnsureCTE(accountExpr string) string {
	return `WITH existing AS (
			SELECT id FROM bss_accounts WHERE account_id = ` + accountExpr + ` ORDER BY created_at, id LIMIT 1),
		created AS (
			INSERT INTO bss_accounts (account_id)
			SELECT ` + accountExpr + ` WHERE NOT EXISTS (SELECT 1 FROM existing)
			RETURNING id),
		acct AS (SELECT id FROM existing UNION ALL SELECT id FROM created) `
}

// OwnSQL returns a statement that makes the account of the given ID (a
// SQL literal, quotes included) belong to the customer passed as $1: the
// existing account of that ID is given to the customer, or a new one is
// registered for it.
func OwnSQL(accountLiteral string) string {
	return `WITH moved AS (
			UPDATE bss_accounts SET customer_id = $1, claimed_by = 'test' WHERE account_id = ` + accountLiteral + ` RETURNING id)
		INSERT INTO bss_accounts (account_id, customer_id, claimed_by)
		SELECT ` + accountLiteral + `, $1, 'test' WHERE NOT EXISTS (SELECT 1 FROM moved)`
}
