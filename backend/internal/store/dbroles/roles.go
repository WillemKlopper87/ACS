// Package dbroles gives each ACS service its own database role with only
// the table privileges its code uses (docs/SECURITY-ASSESSMENT-2026-11-14.md,
// O5). Before this every service connected as the schema owner, so taking
// over any one of them, the internet-facing BSS adapter included, gave
// read and write access to every table: operator password hashes, MFA
// secrets, device credentials, everything.
//
// The roles are optional. A deployment that still uses one database login
// for everything is unaffected: Apply grants only to the roles that exist.
//
// The privileges are a table in manifest.go, generated from the SQL in
// each service's code (extract_test.go) and committed, so a change to it
// is visible in review. The same test fails if a service's code starts
// using a table its role is not granted.
package dbroles

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Service is one ACS process and the database role it connects as.
type Service struct {
	// Cmd is the directory under cmd/.
	Cmd string
	// Role is the database role name.
	Role string
	// DSNEnv is the environment variable the service reads its own
	// connection string from, before falling back to ACS_POSTGRES_DSN.
	DSNEnv string
}

// Services lists the four long-running processes. cmd/migrate is not here:
// it connects as the schema owner, which is the only role that may change
// the schema.
var Services = []Service{
	{Cmd: "acs", Role: "acs_gateway", DSNEnv: "ACS_CWMP_POSTGRES_DSN"},
	{Cmd: "api", Role: "acs_api", DSNEnv: "ACS_API_POSTGRES_DSN"},
	{Cmd: "bssadapter", Role: "acs_bss", DSNEnv: "ACS_BSS_POSTGRES_DSN"},
	{Cmd: "uspc", Role: "acs_usp", DSNEnv: "ACS_USP_POSTGRES_DSN"},
}

// Denied lists tables a service's role is refused although a package it
// links contains SQL for them, because the service never reaches that
// SQL. Each entry names what would have to be called for the service to
// need the table; extract_test.go fails if the service's code starts
// calling it.
var Denied = map[string]map[string][]string{
	// The gateway and the USP controller use internal/bss only to publish
	// device events. OAuth clients and webhook subscriptions (which hold
	// client-secret hashes and webhook signing secrets) are read and
	// written by the adapter and the operator API alone.
	"acs": {
		"bss_oauth_clients":     {`NewOAuthRepository\(`},
		"webhook_subscriptions": {`NewWebhookRepository\(`, `\.ClaimAccount\(`, `\.Stats\(ctx`, `\.Stats\(r\.Context`},
		"webhook_deliveries":    {`NewWebhookRepository\(`, `\.Stats\(ctx`, `\.Stats\(r\.Context`},
	},
	"uspc": {
		"bss_oauth_clients":     {`NewOAuthRepository\(`},
		"webhook_subscriptions": {`NewWebhookRepository\(`, `\.ClaimAccount\(`, `\.Stats\(ctx`, `\.Stats\(r\.Context`},
		"webhook_deliveries":    {`NewWebhookRepository\(`, `\.Stats\(ctx`, `\.Stats\(r\.Context`},
	},
}

// Extra lists privileges the extractor cannot see because the table name
// is not in the SQL text: it is put there from a variable.
var Extra = map[string]map[string][]string{
	// internal/retention builds "DELETE FROM <table>" from its rule list.
	"api": {
		"cwmp_sessions": {"SELECT", "DELETE"}, "audit_log": {"SELECT", "DELETE"}, "parameter_history": {"SELECT", "DELETE"},
		"webhook_deliveries": {"SELECT", "DELETE"}, "jobs": {"SELECT", "DELETE"}, "bulk_operations": {"SELECT", "DELETE"},
		"uploaded_files": {"SELECT", "DELETE"}, "password_reset_tokens": {"SELECT", "DELETE"}, "capture_sessions": {"SELECT", "DELETE"},
		"wifi_samples": {"SELECT", "DELETE"}, "wifi_clients": {"SELECT", "DELETE"}, "account_profile_archive": {"SELECT", "DELETE"},
		// internal/wifiinsights: "DELETE FROM "+table for mesh nodes.
		"wifi_client_samples": {"SELECT", "DELETE"},
	},
	"acs": {
		"wifi_client_samples": {"SELECT", "DELETE"}, "wifi_clients": {"SELECT", "DELETE"},
	},
}

// Apply sets every existing service role's privileges to exactly the
// manifest: everything is revoked first, so a privilege dropped from the
// manifest is dropped from the database on the next run. It must run as
// the schema owner (cmd/migrate does, after migrating). It returns the
// roles it granted to; roles that do not exist are skipped.
func Apply(ctx context.Context, db *sql.DB) ([]string, error) {
	var granted []string
	for _, svc := range Services {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, svc.Role).Scan(&exists); err != nil {
			return granted, fmt.Errorf("look up role %s: %w", svc.Role, err)
		}
		if !exists {
			continue
		}
		if err := applyOne(ctx, db, svc); err != nil {
			return granted, fmt.Errorf("grant %s: %w", svc.Role, err)
		}
		granted = append(granted, svc.Role)
	}
	return granted, nil
}

func applyOne(ctx context.Context, db *sql.DB, svc Service) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Role and table names come from this package's own tables, never from
	// input; they are quoted all the same.
	role := quoteIdent(svc.Role)
	stmts := []string{
		`REVOKE ALL ON ALL TABLES IN SCHEMA public FROM ` + role,
		`REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM ` + role,
		`REVOKE ALL ON SCHEMA public FROM ` + role,
		// Use the schema, never create in it. Databases created before
		// PostgreSQL 15 let every role create objects in public; the owner
		// keeps its own right.
		`REVOKE CREATE ON SCHEMA public FROM PUBLIC`,
		`GRANT USAGE ON SCHEMA public TO ` + role,
		// A sequence holds no data; inserts into tables with serial or
		// identity columns need it.
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + role,
		// Every service checks that the schema is current before it serves.
		`GRANT SELECT ON public.schema_migrations TO ` + role,
	}
	grants := Manifest[svc.Cmd]
	tables := make([]string, 0, len(grants))
	for t := range grants {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		stmts = append(stmts, fmt.Sprintf(`GRANT %s ON public.%s TO %s`, strings.Join(grants[t], ", "), quoteIdent(t), role))
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return tx.Commit()
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
