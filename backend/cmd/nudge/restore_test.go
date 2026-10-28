package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/backup"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

// writeBackup makes a backup of a fresh data directory and returns its path.
func writeBackup(t *testing.T, pass string) string {
	t.Helper()
	ctx := context.Background()
	data := t.TempDir()
	st, err := store.Open(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := secretbox.LoadKey(data); err != nil {
		t.Fatal(err)
	}
	f, _, err := backup.New(backup.Options{Store: st, DataDir: data, StacksDir: filepath.Join(data, "stacks"), Version: "v1"}).Create(ctx, pass)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	path := filepath.Join(t.TempDir(), "b"+backup.Ext)
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, f); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRestoreCommand(t *testing.T) {
	t.Parallel()
	const pass = "correct horse battery"
	archive := writeBackup(t, pass)
	dir := t.TempDir()
	passFile := filepath.Join(dir, "pass")
	if err := os.WriteFile(passFile, []byte(pass+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrongFile := filepath.Join(dir, "wrong")
	if err := os.WriteFile(wrongFile, []byte("not the passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := realMain(context.Background(), append([]string{"restore"}, args...), func(string) string { return "" }, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	if code, _, _ := run("-h"); code != 0 {
		t.Errorf("-h = %d", code)
	}
	if code, _, _ := run("-file", archive); code != 2 {
		t.Errorf("without a passphrase = %d", code)
	}
	if code, _, stderr := run("-file", archive, "-passphrase-file", wrongFile, "-data", data); code != 1 || !strings.Contains(stderr, "wrong passphrase") {
		t.Errorf("wrong passphrase = %d %q", code, stderr)
	}
	code, stdout, stderr := run("-file", archive, "-passphrase-file", passFile, "-data", data)
	if code != 0 || !strings.Contains(stdout, "nudge v1, 0 stacks") {
		t.Fatalf("restore = %d %q %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(data, store.FileName)); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := run("-file", archive, "-passphrase-file", passFile, "-data", data); code != 1 || !strings.Contains(stderr, "-force") {
		t.Errorf("over existing data = %d %q", code, stderr)
	}
	if code, stdout, _ := run("-file", archive, "-passphrase-file", passFile, "-data", data, "-force"); code != 0 || !strings.Contains(stdout, "pre-restore-") {
		t.Errorf("-force = %d %q", code, stdout)
	}
}
