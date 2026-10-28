package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

const pass = "correct horse battery"

var created = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

type source struct {
	svc   *Service
	data  string
	creds environments.Credentials
	box   *secretbox.Box
}

// newSource is a server's data directory: a database with an enrolled agent and a
// sealed value, the key, and two stacks with files that don't belong in a backup.
func newSource(t *testing.T) source {
	t.Helper()
	ctx := context.Background()
	data := t.TempDir()
	st, err := store.Open(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secretbox.LoadKey(data)
	if err != nil {
		t.Fatal(err)
	}
	envs := environments.New(environments.Options{DB: st.DB()})
	_, join, err := envs.CreateAgent(ctx, "remote")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := envs.Enroll(ctx, join.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `CREATE TABLE sealed (v BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO sealed VALUES (?)`, box.Seal([]byte("hunter2"), "test")); err != nil {
		t.Fatal(err)
	}
	stacksDir := filepath.Join(data, "stacks")
	writeFiles(t, stacksDir, map[string]string{
		"1/web/compose.yaml":       "services: {web: {image: nginx}}\n",
		"1/web/.env":               "TAG=1\n",
		"1/web/data/db.sqlite":     "bind-mounted data",
		"2/api/compose.yaml":       "services: {api: {image: api}}\n",
		"2/api/.compose.yaml.tmp":  "half written",
		"check-123/x/compose.yaml": "a draft",
		"1/BAD NAME/compose.yaml":  "not a stack",
	})
	svc := New(Options{Store: st, DataDir: data, StacksDir: stacksDir, Version: "v1.2.3", Now: func() time.Time { return created }})
	svc.kdf = testKDF
	return source{svc: svc, data: data, creds: creds, box: box}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// listFiles returns dir's regular files, relative with slashes.
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	return files
}

func backupBytes(t *testing.T, s source) ([]byte, Manifest) {
	t.Helper()
	f, m, err := s.svc.Create(context.Background(), pass)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return b, m
}

func TestBackupRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := newSource(t)
	archive, m := backupBytes(t, src)
	schema, _ := store.SchemaVersion()
	if m.Version != "v1.2.3" || m.Schema != schema || !m.Created.Equal(created) || m.Stacks != 2 || m.Format != format {
		t.Fatalf("manifest = %+v", m)
	}
	if left, _ := filepath.Glob(filepath.Join(src.data, ".backup-*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}

	dst := filepath.Join(t.TempDir(), "data")
	got, aside, err := Restore(ctx, bytes.NewReader(archive), RestoreOptions{DataDir: dst, StacksDir: filepath.Join(dst, "stacks"), Passphrase: pass})
	if err != nil {
		t.Fatal(err)
	}
	if got != m || aside != "" {
		t.Fatalf("Restore = %+v, %q", got, aside)
	}
	want := []string{"nudge.db", "secret.key", "stacks/1/web/.env", "stacks/1/web/compose.yaml", "stacks/2/api/compose.yaml"}
	if files := listFiles(t, dst); !slices.Equal(files, want) {
		t.Fatalf("restored files = %v, want %v", files, want)
	}
	for _, p := range []string{"nudge.db", "secret.key", "stacks/1/web/.env"} {
		info, err := os.Stat(filepath.Join(dst, p))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v, %v", p, info.Mode(), err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "stacks/1/web/.env")); string(b) != "TAG=1\n" {
		t.Errorf(".env = %q", b)
	}

	// The restored server: the agent's credentials still work and sealed values open.
	st, err := store.Open(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	envs := environments.New(environments.Options{DB: st.DB()})
	if _, err := envs.AuthenticateAgent(ctx, src.creds.AgentID, src.creds.Secret); err != nil {
		t.Fatalf("agent after restore: %v", err)
	}
	box, err := secretbox.LoadKey(dst)
	if err != nil {
		t.Fatal(err)
	}
	var v []byte
	if err := st.DB().QueryRowContext(ctx, `SELECT v FROM sealed`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if p, err := box.Open(v, "test"); err != nil || string(p) != "hunter2" {
		t.Fatalf("sealed value = %q, %v", p, err)
	}
}

func TestRestoreExistingData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	archive, _ := backupBytes(t, newSource(t))
	dst := t.TempDir()
	stacksDir := filepath.Join(dst, "stacks")
	writeFiles(t, dst, map[string]string{"nudge.db": "old db", "nudge.db-wal": "old wal", "secret.key": "old key", "stacks/9/old/compose.yaml": "old"})
	o := RestoreOptions{DataDir: dst, StacksDir: stacksDir, Passphrase: pass, Now: func() time.Time { return created }}

	if _, _, err := Restore(ctx, bytes.NewReader(archive), o); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("without force: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "nudge.db")); string(b) != "old db" {
		t.Fatal("refused restore changed the database")
	}

	o.Force = true
	_, aside, err := Restore(ctx, bytes.NewReader(archive), o)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dst, "pre-restore-20261028-120000"); aside != want {
		t.Fatalf("aside = %q, want %q", aside, want)
	}
	want := []string{
		"nudge.db", "pre-restore-20261028-120000/nudge.db", "pre-restore-20261028-120000/nudge.db-wal",
		"pre-restore-20261028-120000/secret.key", "secret.key",
		"stacks.pre-restore-20261028-120000/9/old/compose.yaml",
		"stacks/1/web/.env", "stacks/1/web/compose.yaml", "stacks/2/api/compose.yaml",
	}
	if files := listFiles(t, dst); !slices.Equal(files, want) {
		t.Fatalf("files = %v\nwant %v", files, want)
	}
}

func TestRestoreRefusesBadArchives(t *testing.T) {
	t.Parallel()
	archive, _ := backupBytes(t, newSource(t))
	tests := []struct {
		name    string
		archive []byte
		pass    string
		want    error
	}{
		{"wrong passphrase", archive, "not the passphrase", ErrDecrypt},
		{"cut", archive[:len(archive)/2], pass, ErrDecrypt},
		{"not a backup", []byte("hello"), pass, ErrNotBackup},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dst := t.TempDir()
			_, _, err := Restore(context.Background(), bytes.NewReader(tt.archive), RestoreOptions{DataDir: dst, StacksDir: filepath.Join(dst, "stacks"), Passphrase: tt.pass})
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if files := listFiles(t, dst); len(files) != 0 {
				t.Fatalf("a failed restore left %v", files)
			}
			if entries, _ := os.ReadDir(dst); len(entries) != 0 {
				t.Fatalf("a failed restore left %v", entries)
			}
		})
	}
}

func TestCreateRefuses(t *testing.T) {
	t.Parallel()
	src := newSource(t)
	if _, _, err := src.svc.Create(context.Background(), "short"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("short passphrase: %v", err)
	}
	src.svc.mu.Lock()
	_, _, err := src.svc.Create(context.Background(), pass)
	src.svc.mu.Unlock()
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("while busy: %v", err)
	}
}

func TestSaveKeepsNewest(t *testing.T) {
	t.Parallel()
	src := newSource(t)
	now := created
	src.svc.now = func() time.Time { return now }
	var paths []string
	for range 4 {
		p, err := src.svc.Save(context.Background(), pass, 2)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.Base(p))
		now = now.Add(time.Hour)
	}
	entries, err := os.ReadDir(filepath.Join(src.data, Dir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := paths[2:]; !slices.Equal(names, want) {
		t.Fatalf("backups = %v, want %v", names, want)
	}
	if paths[0] != "nudge-20261028-120000.nudgebak" {
		t.Fatalf("name = %q", paths[0])
	}
}
