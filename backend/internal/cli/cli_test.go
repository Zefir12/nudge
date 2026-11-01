package cli

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPrepareDataDir(t *testing.T) {
	t.Parallel()
	t.Run("creates missing directory with 0700", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "a", "data")
		if err := PrepareDataDir(dir); err != nil {
			t.Fatalf("PrepareDataDir: %v", err)
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
		if err := PrepareDataDir(filepath.Join(file, "data")); err == nil {
			t.Fatal("PrepareDataDir under a file succeeded")
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
		err := PrepareDataDir(dir)
		if err == nil || !strings.Contains(err.Error(), "not writable") {
			t.Fatalf("err = %v, want a not writable error", err)
		}
	})
}

func TestCleanComposeConfigs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		stale   []string
		wantLog bool
	}{
		{"nothing left", nil, false},
		{"stale configs", []string{"nudge-compose-1", "nudge-compose-2"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := ComposeConfigDir(t.TempDir())
			for _, name := range tt.stale {
				if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			if err := CleanComposeConfigs(dir, slog.New(slog.NewTextHandler(&buf, nil))); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Errorf("left %v, %v", entries, err)
			}
			if logged := strings.Contains(buf.String(), "count="+strconv.Itoa(len(tt.stale))); logged != tt.wantLog {
				t.Errorf("log %q, want a count: %v", buf.String(), tt.wantLog)
			}
		})
	}
}

func TestProbe(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)
	tests := []struct {
		args []string
		code int
	}{
		{[]string{srv.URL + "/health"}, 0},
		{[]string{"-status", "503", srv.URL + "/down"}, 0},
		{[]string{srv.URL + "/down"}, 3},
		{[]string{"-timeout", "2m", srv.URL}, 2},
		{nil, 2},
	}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := Probe(context.Background(), tt.args, &stdout, &stderr); code != tt.code {
			t.Errorf("probe %v = %d (%s%s), want %d", tt.args, code, &stdout, &stderr, tt.code)
		}
	}
}
