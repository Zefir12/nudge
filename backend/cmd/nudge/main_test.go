package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

func TestRealMain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version", args: []string{"version"}, wantCode: 0, wantStdout: "dev"},
		{name: "help", args: []string{"help"}, wantCode: 0, wantStdout: "usage: nudge"},
		{name: "serve help", args: []string{"serve", "-h"}, wantCode: 0, wantStderr: "-addr"},
		{name: "default command is serve", args: []string{"-h"}, wantCode: 0, wantStderr: "-addr"},
		{name: "unknown command", args: []string{"frobnicate"}, wantCode: 2, wantStderr: `unknown command "frobnicate"`},
		{name: "bad flag", args: []string{"serve", "-log-level", "loud"}, wantCode: 2, wantStderr: "log level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			getenv := func(string) string { return "" }
			code := realMain(context.Background(), tt.args, getenv, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestServeStopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	getenv := func(string) string { return "" }
	if code := realMain(ctx, []string{"serve", "-addr", "127.0.0.1:0", "-data", t.TempDir()}, getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, want 0 (stderr %q)", code, stderr.String())
	}
}

func TestPrepareDataDir(t *testing.T) {
	t.Parallel()
	t.Run("creates missing directory with 0700", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "a", "data")
		if err := prepareDataDir(dir); err != nil {
			t.Fatalf("prepareDataDir: %v", err)
		}
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o700 {
			t.Errorf("mode = %o, want 700", perm)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("write test left %d files behind", len(entries))
		}
	})
	t.Run("path is a file", func(t *testing.T) {
		t.Parallel()
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := prepareDataDir(filepath.Join(file, "data")); err == nil {
			t.Fatal("prepareDataDir under a file succeeded")
		}
	})
	t.Run("not writable", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root can write to read-only directories")
		}
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		err := prepareDataDir(dir)
		if err == nil || !strings.Contains(err.Error(), "not writable") {
			t.Fatalf("err = %v, want a not writable error", err)
		}
	})
}

func TestServeCreatesDatabase(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := filepath.Join(t.TempDir(), "data")
	var stdout, stderr bytes.Buffer
	getenv := func(string) string { return "" }
	if code := realMain(ctx, []string{"serve", "-addr", "127.0.0.1:0", "-data", dir}, getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, store.FileName)); err != nil {
		t.Errorf("database not created: %v", err)
	}
}

func TestServeFailsOnBadDataDir(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	getenv := func(string) string { return "" }
	if code := realMain(context.Background(), []string{"serve", "-addr", "127.0.0.1:0", "-data", file}, getenv, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, want 1 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "data directory") {
		t.Errorf("stderr %q does not mention the data directory", stderr.String())
	}
}
