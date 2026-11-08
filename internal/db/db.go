// Package db opens the Postgres pool and applies embedded SQL migrations.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open connects to Postgres and runs any pending migrations.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// Migrate applies migrations/*.sql in filename order, once each. The first
// migration creates schema_migrations itself, so the bookkeeping table is
// created with "if not exists" and consulted only after it exists.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	sort.Strings(names)
	if _, err := pool.Exec(ctx, `create table if not exists schema_migrations (version text primary key, applied_at timestamptz not null default now())`); err != nil {
		return fmt.Errorf("db: migrations table: %w", err)
	}
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		var applied bool
		if err := pool.QueryRow(ctx, `select exists(select 1 from schema_migrations where version=$1)`, version).Scan(&applied); err != nil {
			return fmt.Errorf("db: %w", err)
		}
		if applied {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("db: %w", err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("db: %w", err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("db: migration %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, `insert into schema_migrations (version) values ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("db: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("db: %w", err)
		}
	}
	return nil
}
