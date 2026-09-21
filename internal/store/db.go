// Package store provides SQLite persistence for the council.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps a SQLite connection pool with migration support.
type DB struct {
	db *sql.DB
}

// Open opens the SQLite database at path, sets pragmas, and applies
// pending migrations. It is idempotent: re-running applies nothing new.
func Open(path string) (*DB, error) {
	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	// Single-writer: the council runs concurrent goroutines against one
	// file. Serialising connections avoids SQLITE_BUSY on parallel inserts
	// without any retry logic in callers.
	sqldb.SetMaxOpenConns(1)
	d := &DB{db: sqldb}
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, p := range pragmas {
		if _, err := d.db.Exec(p); err != nil {
			_ = d.db.Close()
			return nil, fmt.Errorf("store: pragma %q: %w", p, err)
		}
	}
	if err := d.migrate(); err != nil {
		_ = d.db.Close()
		return nil, err
	}
	return d, nil
}

// Close releases the underlying connection pool.
func (db *DB) Close() error {
	return db.db.Close()
}

// WithTx runs fn inside a transaction: commit on nil, rollback on error.
func (db *DB) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("store: rollback: %v (cause: %w)", rbErr, err)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit tx: %w", err)
	}
	return nil
}

// AppliedMigrations returns the versions already recorded.
func (db *DB) AppliedMigrations() ([]string, error) {
	rows, err := db.db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("store: list migrations: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("store: scan migration: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (db *DB) migrate() error {
	if _, err := db.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	applied := map[string]bool{}
	rows, err := db.db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("store: scan version: %w", err)
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: rows: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("store: read migrations dir: %w", err)
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		if applied[version] {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", name, err)
		}
		tx, err := db.db.Begin()
		if err != nil {
			return fmt.Errorf("store: begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %s: %w", name, err)
		}
	}
	return nil
}

// nowUTC returns the current time formatted as RFC3339 UTC text.
func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// boolInt converts a bool to 0/1 for SQLite storage.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
