package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"acs/internal/store"
)

// The Grafana dashboards in infra/ query Postgres directly, which buys
// per-device fault-finding that Prometheus label cardinality cannot
// support — at the cost that a migration can silently break a panel. A
// broken panel renders "No data", not an error, so nobody finds out until
// an operator needs it during an incident.
//
// This is the drift gate for that, in the same spirit as
// cmd/api's TestOpenAPIMatchesRegisteredRoutes: every rawSql in every
// dashboard is executed against a freshly migrated schema. A renamed
// column or dropped table fails here instead of in production.
//
// Gated on ACS_TEST_POSTGRES_DSN like the other DB-backed suites; CI
// defines it.

const dashboardDir = "../../../infra/grafana/provisioning/dashboards/json"

type dashboard struct {
	UID    string `json:"uid"`
	Title  string `json:"title"`
	Panels []struct {
		ID      int    `json:"id"`
		Title   string `json:"title"`
		Targets []struct {
			RawSQL string `json:"rawSql"`
		} `json:"targets"`
	} `json:"panels"`
	Templating struct {
		List []struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			Query any    `json:"query"`
		} `json:"list"`
	} `json:"templating"`
}

// Grafana interpolates ${var:sqlstring} before the query reaches Postgres.
// Substituting an empty quoted string keeps the statement valid and
// parseable while matching no rows, which is all this test needs: it is
// checking that the columns and tables exist, not what they contain.
var sqlStringVar = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*:sqlstring\}`)

func interpolate(q string) string { return sqlStringVar.ReplaceAllString(q, `''`) }

type panelQuery struct {
	dashboard string
	panel     string
	sql       string
}

func loadDashboardQueries(t *testing.T) []panelQuery {
	t.Helper()
	entries, err := os.ReadDir(dashboardDir)
	if err != nil {
		t.Fatalf("read dashboard dir: %v", err)
	}

	var queries []panelQuery
	var dashboards int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dashboardDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var d dashboard
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("%s is not valid dashboard JSON: %v", e.Name(), err)
		}
		if d.UID == "" || d.Title == "" {
			t.Errorf("%s has no uid/title — Grafana provisioning needs both to keep a stable URL", e.Name())
		}
		dashboards++
		for _, p := range d.Panels {
			for _, tgt := range p.Targets {
				if strings.TrimSpace(tgt.RawSQL) == "" {
					continue
				}
				queries = append(queries, panelQuery{d.Title, p.Title, interpolate(tgt.RawSQL)})
			}
		}
		for _, v := range d.Templating.List {
			if v.Type != "query" {
				continue
			}
			// SQL datasources take the structured form ({"rawSql": ...});
			// a bare string is the shape Prometheus-style sources use.
			// Accept both so this gate does not quietly skip a variable.
			var q string
			switch raw := v.Query.(type) {
			case string:
				q = raw
			case map[string]any:
				q, _ = raw["rawSql"].(string)
			}
			if strings.TrimSpace(q) == "" {
				continue
			}
			queries = append(queries, panelQuery{d.Title, "variable: " + v.Name, interpolate(q)})
		}
	}

	if dashboards == 0 {
		t.Fatalf("no dashboards found in %s — the provisioning directory moved?", dashboardDir)
	}
	return queries
}

// TestDashboardJSONIsWellFormed runs without a database: it is the cheap
// half of the gate, so a malformed dashboard is caught by the normal
// `go test ./...` even when no Postgres is configured.
func TestDashboardJSONIsWellFormed(t *testing.T) {
	queries := loadDashboardQueries(t)
	if len(queries) == 0 {
		t.Fatal("no SQL-backed panels found — expected the tenancy/BSS/fleet dashboards")
	}
	for _, q := range queries {
		if strings.Contains(q.sql, "${") {
			t.Errorf("[%s] %s: unsubstituted Grafana variable left in SQL — only the "+
				":sqlstring formatter is understood by this gate:\n%s", q.dashboard, q.panel, q.sql)
		}
	}
}

func TestDashboardSQLMatchesSchema(t *testing.T) {
	dsn := os.Getenv("ACS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ACS_TEST_POSTGRES_DSN not set — skipping DB-backed dashboard SQL gate")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	for _, q := range loadDashboardQueries(t) {
		t.Run(q.dashboard+"/"+q.panel, func(t *testing.T) {
			// Execute rather than merely prepare: Postgres defers some
			// checks (and these queries are read-only against an empty
			// schema, so running them is cheap and catches more).
			rows, err := db.QueryContext(ctx, q.sql)
			if err != nil {
				t.Fatalf("panel query no longer matches the schema: %v\n\n%s", err, q.sql)
			}
			defer rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatalf("panel query failed mid-scan: %v", err)
			}
		})
	}
}

// Guards the security property of scripts/grafana-db-role.sh: the Grafana
// role must never be able to read a secret. If a dashboard starts
// selecting one of these columns the panel would break at runtime with a
// permission error — catch it here, where the reason is obvious.
func TestDashboardSQLAvoidsSecretColumns(t *testing.T) {
	forbidden := []struct{ column, why string }{
		{"secret", "webhook_subscriptions.secret is the outbound HMAC signing key"},
		{"payload", "jobs.payload / webhook_deliveries.payload can carry RPC arguments such as a WPA key"},
		{"result_detail", "jobs.result_detail can carry parameter values read back from a CPE"},
		{"password", "credential tables are not readable by grafana_ro"},
		{"password_hash", "operators.password_hash is not readable by grafana_ro"},
	}
	for _, q := range loadDashboardQueries(t) {
		lowered := strings.ToLower(q.sql)
		for _, f := range forbidden {
			if regexp.MustCompile(`\b` + f.column + `\b`).MatchString(lowered) {
				t.Errorf("[%s] %s selects %q — %s", q.dashboard, q.panel, f.column, f.why)
			}
		}
	}
}

var _ = sql.ErrNoRows
