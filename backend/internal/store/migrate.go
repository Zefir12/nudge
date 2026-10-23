package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationsDir is the directory of migrationsFS that holds the migrations.
const migrationsDir = "migrations"

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads the NNNN_name.sql files in dir, sorted by version.
func loadMigrations(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var ms []migration
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %s: name must be NNNN_name.sql", e.Name())
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(b)})
	}
	slices.SortFunc(ms, func(a, b migration) int { return a.version - b.version })
	for i := 1; i < len(ms); i++ {
		if ms[i].version == ms[i-1].version {
			return nil, fmt.Errorf("migrations %s and %s share version %d", ms[i-1].name, ms[i].name, ms[i].version)
		}
	}
	return ms, nil
}

// migrate applies the migrations in dir of fsys that are not yet recorded in
// schema_migrations, each in its own transaction. The transactions take the write lock
// up front (BEGIN IMMEDIATE, see dsn) and check the version inside it, so concurrent
// migrates of the same file wait for each other and apply each migration once.
func migrate(ctx context.Context, db *sql.DB, fsys fs.FS, dir string) error {
	ms, err := loadMigrations(fsys, dir)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at INTEGER NOT NULL DEFAULT (unixepoch())
	) STRICT`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var latest int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&latest); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if len(ms) > 0 && latest > ms[len(ms)-1].version {
		return fmt.Errorf("%w: database at version %d, binary knows up to %d", errNewerSchema, latest, ms[len(ms)-1].version)
	}
	for _, m := range ms {
		if err := apply(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

func apply(ctx context.Context, db *sql.DB, m migration) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %s: begin: %w", m.name, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&n); err != nil {
		return fmt.Errorf("migration %s: check: %w", m.name, err)
	}
	if n > 0 {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %s: commit: %w", m.name, err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, m.version, m.name); err != nil {
		return fmt.Errorf("migration %s: record: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: commit: %w", m.name, err)
	}
	return nil
}
