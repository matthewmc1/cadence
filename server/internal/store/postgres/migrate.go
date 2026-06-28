package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cadence/server/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey serializes migrators across instances so a rolling deploy
// can't race two pods applying the same migration.
const advisoryLockKey = 4736251

// Migrate applies every pending *.up.sql migration in order, inside a single
// session holding a global advisory lock. Each migration runs in its own
// transaction and is recorded in schema_migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (applied []string, err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, advisoryLockKey)

	if _, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, fmt.Errorf("ensure schema_migrations: %w", err)
	}

	done := map[string]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		done[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	versions, err := upMigrations()
	if err != nil {
		return nil, err
	}

	for _, m := range versions {
		if done[m.version] {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return applied, err
		}
		if _, err = tx.Exec(ctx, m.sql); err != nil {
			tx.Rollback(ctx)
			return applied, fmt.Errorf("apply %s: %w", m.version, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.version); err != nil {
			tx.Rollback(ctx)
			return applied, err
		}
		if err = tx.Commit(ctx); err != nil {
			return applied, err
		}
		applied = append(applied, m.version)
	}
	return applied, nil
}

type migration struct {
	version string
	sql     string
}

func upMigrations() ([]migration, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		b, err := migrations.FS.ReadFile(name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{
			version: strings.TrimSuffix(name, ".up.sql"),
			sql:     string(b),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
