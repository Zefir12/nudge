// Package store is nudge's SQLite database: it opens <data>/nudge.db and applies the
// numbered migrations embedded from migrations/.
//
// Pending migrations run together in one transaction, so PRAGMA foreign_keys=OFF has no
// effect in them: a table rebuild must keep the rows its references point to valid.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"

	// Registers the pure-Go "sqlite" driver, so the binary stays static.
	_ "modernc.org/sqlite"
)

// FileName is the database file inside the data directory.
const FileName = "nudge.db"

// busyTimeoutMS is how long a statement waits for another connection's write lock
// (another process, or a concurrent migrate) before failing with SQLITE_BUSY. Concurrent
// migrates queue for the lock, so the last one waits for all the others: on a loaded
// machine 5s was not enough for eight of them.
const busyTimeoutMS = 30000

// Store owns the database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) <dataDir>/nudge.db and applies pending migrations.
// The data directory must already exist.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	db, err := OpenDB(ctx, filepath.Join(dataDir, FileName), migrationsFS, migrationsDir)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// OpenDB opens (creating if needed) the SQLite database at path with nudge's settings
// (WAL, foreign keys, busy timeout, BEGIN IMMEDIATE) and applies the NNNN_name.sql
// migrations in dir of fsys that it lacks. Packages with a database of their own (the
// metrics) open it with their own migrations.
func OpenDB(ctx context.Context, path string, fsys fs.FS, dir string) (*sql.DB, error) {
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
	if err := migrate(ctx, db, fsys, dir); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return db, nil
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

// SchemaVersion is the newest migration this binary has, the schema version a database
// it opened is at.
func SchemaVersion() (int, error) {
	ms, err := loadMigrations(migrationsFS, migrationsDir)
	if err != nil {
		return 0, err
	}
	if len(ms) == 0 {
		return 0, nil
	}
	return ms[len(ms)-1].version, nil
}

// Snapshot writes a consistent copy of the database to path, which must not exist:
// VACUUM INTO reads one snapshot, so writes going on meanwhile are either all in the
// copy or not at all, and the copy has no WAL file to go with it.
func (s *Store) Snapshot(ctx context.Context, path string) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	return nil
}
