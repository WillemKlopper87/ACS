package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

type MigrationCheck struct {
	Filename string `json:"filename"`
	State    string `json:"state"`
}

// CheckMigrations inspects embedded migration checksums in a read-only snapshot.
// It never creates the tracking table, backfills checksums or applies migrations.
func CheckMigrations(ctx context.Context, db *sql.DB) ([]MigrationCheck, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, err
	}
	applied := map[string]string{}
	if exists {
		var hasChecksum bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='schema_migrations' AND column_name='checksum')`).Scan(&hasChecksum); err != nil {
			return nil, err
		}
		query := `SELECT filename, COALESCE(checksum,'') FROM public.schema_migrations`
		if !hasChecksum {
			query = `SELECT filename, '' FROM public.schema_migrations`
		}
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name, sum string
			if err := rows.Scan(&name, &sum); err != nil {
				rows.Close()
				return nil, err
			}
			applied[name] = sum
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	out := []MigrationCheck{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		state := "pending"
		if sum, ok := applied[entry.Name()]; ok {
			switch {
			case sum == "":
				state = "unverified"
			case sum == checksum(raw):
				state = "applied"
			default:
				state = "checksum_mismatch"
			}
		}
		out = append(out, MigrationCheck{entry.Name(), state})
		delete(applied, entry.Name())
	}
	for name := range applied {
		out = append(out, MigrationCheck{name, "unknown_to_binary"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Filename < out[j].Filename })
	return out, tx.Commit()
}
