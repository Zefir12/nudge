package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func openTest(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenSettings(t *testing.T) {
	t.Parallel()
	db := openTest(t, t.TempDir()).DB()
	tests := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", strconv.Itoa(busyTimeoutMS)},
	}
	for _, tt := range tests {
		var got string
		if err := db.QueryRow("PRAGMA " + tt.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", tt.pragma, err)
		}
		if got != tt.want {
			t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
		}
	}
	if n := db.Stats().MaxOpenConnections; n != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", n)
	}
}

func TestOpenCreatesTables(t *testing.T) {
	t.Parallel()
	db := openTest(t, t.TempDir()).DB()
	for _, table := range []string{"schema_migrations", "users", "sessions", "api_tokens", "environments"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing", table)
		}
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	t.Parallel()
	db := openTest(t, t.TempDir()).DB()
	_, err := db.Exec(`INSERT INTO sessions (id_hash, user_id, created_at, last_seen_at, expires_at) VALUES ('h', 42, 0, 0, 0)`)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("insert session for missing user: err = %v, want a foreign key error", err)
	}
}

func TestReopenIsNoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := openTest(t, dir)
	if _, err := s.DB().Exec(`INSERT INTO environments (name, kind, created_at) VALUES ('local', 'local', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := openTest(t, dir).DB()
	var envs, migrations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM environments`).Scan(&envs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if want := embeddedMigrations(t); envs != 1 || migrations != want {
		t.Errorf("after reopen: %d environments, %d migrations; want 1 and %d", envs, migrations, want)
	}
}

func TestConcurrentOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			s, err := Open(context.Background(), dir)
			if err == nil {
				err = s.Close()
			}
			errs[i] = err
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Open %d: %v", i, err)
		}
	}
	db := openTest(t, dir).DB()
	var migrations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if want := embeddedMigrations(t); migrations != want {
		t.Errorf("schema_migrations has %d rows, want %d", migrations, want)
	}
}

// embeddedMigrations counts the migrations nudge ships.
func embeddedMigrations(t *testing.T) int {
	t.Helper()
	entries, err := migrationsFS.ReadDir(migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestOpenMissingDir(t *testing.T) {
	t.Parallel()
	if _, err := Open(context.Background(), t.TempDir()+"/missing"); err == nil {
		t.Fatal("Open of a missing directory succeeded")
	}
}

func rawDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(t.TempDir()+"/test.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigrate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   fstest.MapFS
		wantErr string
		tables  []string
	}{
		{
			name: "applies in version order",
			files: fstest.MapFS{
				"m/0010_b.sql":  {Data: []byte(`CREATE TABLE b (id INTEGER REFERENCES a (id));`)},
				"m/0002_a.sql":  {Data: []byte(`CREATE TABLE a (id INTEGER PRIMARY KEY); CREATE TABLE a2 (x);`)},
				"m/README.md":   {Data: []byte(`ignored`)},
				"m/sub/0001.go": {Data: []byte(`ignored`)},
			},
			tables: []string{"a", "a2", "b"},
		},
		{
			name:    "bad name",
			files:   fstest.MapFS{"m/init.sql": {Data: []byte(`SELECT 1;`)}},
			wantErr: "NNNN_name.sql",
		},
		{
			name: "duplicate version",
			files: fstest.MapFS{
				"m/0001_a.sql": {Data: []byte(`SELECT 1;`)},
				"m/1_b.sql":    {Data: []byte(`SELECT 1;`)},
			},
			wantErr: "share version 1",
		},
		{
			name: "failed migration rolls back",
			files: fstest.MapFS{
				"m/0001_a.sql": {Data: []byte(`CREATE TABLE a (id); CREATE TABLE oops (`)},
			},
			wantErr: "0001_a.sql",
		},
		{
			name: "failed migration rolls back the ones before it",
			files: fstest.MapFS{
				"m/0001_a.sql": {Data: []byte(`CREATE TABLE a (id);`)},
				"m/0002_b.sql": {Data: []byte(`CREATE TABLE oops (`)},
			},
			wantErr: "0002_b.sql",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := rawDB(t)
			err := migrate(context.Background(), db, tt.files, "m")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("migrate: err = %v, want it to contain %q", err, tt.wantErr)
				}
				var n int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'a'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Error("table from a failed migration was kept")
				}
				return
			}
			if err != nil {
				t.Fatalf("migrate: %v", err)
			}
			for _, table := range tt.tables {
				if _, err := db.Exec(`SELECT * FROM ` + table); err != nil {
					t.Errorf("table %s: %v", table, err)
				}
			}
			if err := migrate(context.Background(), db, tt.files, "m"); err != nil {
				t.Fatalf("second migrate: %v", err)
			}
		})
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	t.Parallel()
	db := rawDB(t)
	v2 := fstest.MapFS{
		"m/0001_a.sql": {Data: []byte(`CREATE TABLE a (id);`)},
		"m/0002_b.sql": {Data: []byte(`CREATE TABLE b (id);`)},
	}
	if err := migrate(context.Background(), db, v2, "m"); err != nil {
		t.Fatal(err)
	}
	v1 := fstest.MapFS{"m/0001_a.sql": v2["m/0001_a.sql"]}
	if err := migrate(context.Background(), db, v1, "m"); !errors.Is(err, errNewerSchema) {
		t.Fatalf("migrate with older binary: err = %v, want errNewerSchema", err)
	}
}

func TestOpenPathWithURISyntax(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() + "/we?ird#dir%20"
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	openTest(t, dir)
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatalf("database not at %s: %v", dir, err)
	}
}
