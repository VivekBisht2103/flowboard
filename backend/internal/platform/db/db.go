// Package db holds the Postgres connection, transaction helper and migrator.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/flowboard/backend/migrations"
)

// Q is satisfied by *sql.DB and *sql.Tx so repositories work in or out of a transaction.
type Q interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Open connects and waits (bounded) for the database to accept connections.
// If the URL names no sslmode, TLS is tried first (lib/pq's default) and, when
// the server has TLS off (e.g. a private network), it falls back to sslmode=disable.
func Open(ctx context.Context, url string) (*sql.DB, error) {
	d, err := open(ctx, url)
	if err != nil && !strings.Contains(url, "sslmode=") && strings.Contains(err.Error(), "SSL is not enabled") {
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		return open(ctx, url+sep+"sslmode=disable")
	}
	return d, err
}

func open(ctx context.Context, url string) (*sql.DB, error) {
	d, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(20)
	d.SetMaxIdleConns(10)
	d.SetConnMaxLifetime(30 * time.Minute)
	deadline := time.Now().Add(60 * time.Second)
	for {
		err = d.PingContext(ctx)
		if err == nil {
			return d, nil
		}
		if strings.Contains(err.Error(), "SSL is not enabled") {
			d.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("database not reachable: %w", err)
		}
		time.Sleep(time.Second)
	}
}

// InTx runs fn in a transaction, committing on nil error.
func InTx(ctx context.Context, d *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Migrate applies embedded *.sql files in lexical order, once each.
// An advisory lock makes concurrent starts (several API replicas) safe.
func Migrate(ctx context.Context, d *sql.DB) error {
	return InTx(ctx, d, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(727274)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return err
		}
		entries, err := fs.Glob(migrations.FS, "*.sql")
		if err != nil {
			return err
		}
		sort.Strings(entries)
		for _, name := range entries {
			version := strings.TrimSuffix(name, ".sql")
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			body, err := migrations.FS.ReadFile(name)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
				return err
			}
		}
		return nil
	})
}

// ConfigLock serialises hierarchy-config mutations (statuses, fields, containers)
// for a workspace so cross-scope invariants (key uniqueness, resolved-set validity)
// are checked against a stable snapshot.
func ConfigLock(ctx context.Context, tx *sql.Tx, workspaceID string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('config:' || $1))`, workspaceID)
	return err
}
