// Package store is nudge's SQLite database: it opens <data>/nudge.db and applies the
// numbered migrations embedded from migrations/.
//
// Each migration runs in one transaction, so PRAGMA foreign_keys=OFF has no effect in
// one: a table rebuild must keep the rows its references point to valid.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	// Registers the pure-Go "sqlite" driver, so the binary stays static.
	_ "modernc.org/sqlite"
)

// FileName is the database file inside the data directory.
const FileName = "nudge.db"

// busyTimeoutMS is how long a statement waits for another connection's write lock
// (another process, or a concurrent migrate) before failing with SQLITE_BUSY.
const busyTimeoutMS = 5000

// Store owns the database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) <dataDir>/nudge.db and applies pending migrations.
// The data directory must already exist.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	path := filepath.Join(dataDir, FileName)
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// One connection: SQLite allows a single writer anyway, and one connection means
	// the pragmas and BEGIN IMMEDIATE apply to every query without pool surprises.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := migrate(ctx, db, migrationsFS, migrationsDir); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// dsn sets the busy timeout first, so the WAL switch also waits for other writers, and
// makes every transaction BEGIN IMMEDIATE so read-then-write transactions never fail
// with SQLITE_BUSY halfway.
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	// Opaque keeps a path with '?', '#' or '%' from being read as URI syntax.
	return (&url.URL{Scheme: "file", Opaque: url.PathEscape(path), RawQuery: q.Encode()}).String()
}

// DB returns the handle for the packages that query their own tables.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	return nil
}

// errNewerSchema means the database was migrated by a newer nudge.
var errNewerSchema = errors.New("database schema is newer than this binary")
