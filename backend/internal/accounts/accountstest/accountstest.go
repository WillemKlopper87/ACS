// Package accountstest helps test fixtures that write account-keyed rows
// directly, bypassing the repositories.
package accountstest

// EnsureCTE returns the opening of a statement that registers the account
// named by the SQL expression accountExpr (a literal or a parameter) if it
// is new, and exposes its key as (SELECT id FROM acct). Put it in front of
// an INSERT that needs account_key:
//
//	accountstest.EnsureCTE("$1") + `INSERT INTO account_device_mappings
//	    (id, account_id, account_key, ...) VALUES ($2, $1, (SELECT id FROM acct), ...)`
//
// An account that already exists, owned or not, is used as it is.
func EnsureCTE(accountExpr string) string {
	return `WITH acct AS (
		INSERT INTO bss_accounts (account_id) VALUES (` + accountExpr + `)
		ON CONFLICT (account_id) DO UPDATE SET account_id = EXCLUDED.account_id
		RETURNING id) `
}
