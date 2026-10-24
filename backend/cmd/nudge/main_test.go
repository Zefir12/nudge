package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/config"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
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
		{name: "agent help", args: []string{"agent", "-h"}, wantCode: 0, wantStderr: "-join-token"},
		{name: "agent without server", args: []string{"agent"}, wantCode: 2, wantStderr: "server URL is required"},
		{name: "agent refuses http", args: []string{"agent", "-server", "http://nudge.example.net"}, wantCode: 2, wantStderr: "-insecure"},
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
	if code := realMain(ctx, []string{"serve", "-addr", "127.0.0.1:0", "-docker-host", noDocker(t), "-data", t.TempDir()}, getenv, &stdout, &stderr); code != 0 {
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
	if code := realMain(ctx, []string{"serve", "-addr", "127.0.0.1:0", "-docker-host", noDocker(t), "-data", dir}, getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, store.FileName)); err != nil {
		t.Errorf("database not created: %v", err)
	}
}

func TestServeAdminPassword(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		password string
		wantCode int
		wantLog  string
	}{
		{name: "creates admin", password: "longenough", wantCode: 0, wantLog: "created admin user"},
		{name: "too short", password: "short", wantCode: 1, wantLog: "NUDGE_ADMIN_PASSWORD: create admin: password must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// A cancelled context: serve still finishes startup, then shuts down.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			dir := t.TempDir()
			var stdout, stderr bytes.Buffer
			getenv := func(k string) string {
				if k == "NUDGE_ADMIN_PASSWORD" {
					return tt.password
				}
				return ""
			}
			if code := realMain(ctx, []string{"serve", "-addr", "127.0.0.1:0", "-docker-host", noDocker(t), "-data", dir}, getenv, &stdout, &stderr); code != tt.wantCode {
				t.Fatalf("code = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantLog) {
				t.Errorf("stderr %q does not contain %q", stderr.String(), tt.wantLog)
			}
			if strings.Contains(stderr.String(), tt.password) {
				t.Error("password logged")
			}
		})
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
	if code := realMain(context.Background(), []string{"serve", "-addr", "127.0.0.1:0", "-docker-host", noDocker(t), "-data", file}, getenv, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, want 1 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "data directory") {
		t.Errorf("stderr %q does not mention the data directory", stderr.String())
	}
}

func TestAgentWithoutCredentials(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	getenv := func(string) string { return "" }
	args := []string{"agent", "-server", "http://127.0.0.1:1", "-insecure", "-docker-host", noDocker(t), "-data", t.TempDir()}
	if code := realMain(context.Background(), args, getenv, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, want 1 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no join token") {
		t.Errorf("stderr = %q, want the missing join token explained", stderr.String())
	}
}

func TestServeFailsOnBadCertificate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(cert, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	getenv := func(string) string { return "" }
	args := []string{"serve", "-addr", "127.0.0.1:0", "-docker-host", noDocker(t), "-data", t.TempDir(), "-tls-cert", cert, "-tls-key", filepath.Join(dir, "missing.pem")}
	if code := realMain(context.Background(), args, getenv, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, want 1 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "TLS") {
		t.Errorf("stderr %q does not mention TLS", stderr.String())
	}
}

// noDocker is a Docker host that doesn't exist, so serve never reaches the host's daemon.
func noDocker(t *testing.T) string {
	t.Helper()
	return "unix://" + filepath.Join(t.TempDir(), "none.sock")
}

func TestServeCreatesLocalEnvironment(t *testing.T) {
	t.Parallel()
	// A fake Docker API on a unix socket: every request gets a 200.
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Api-Version", "1.51")
		_, _ = io.WriteString(w, "OK")
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	getenv := func(k string) string {
		if k == "DOCKER_HOST" {
			return "unix://" + sock
		}
		return ""
	}
	if code := realMain(ctx, []string{"serve", "-addr", "127.0.0.1:0", "-data", t.TempDir()}, getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "created local environment") {
		t.Errorf("stderr %q does not report the local environment", stderr.String())
	}
}

func TestStackRunner(t *testing.T) {
	t.Parallel()
	runner := stackRunner(config.Server{StacksDir: t.TempDir(), Compose: "/bin/compose", DockerHost: "unix:///docker.sock"})
	if runner(environments.Environment{ID: 1, Kind: environments.KindLocal}) == nil {
		t.Error("no runner for the local environment")
	}
	if runner(environments.Environment{ID: 2, Kind: environments.KindAgent}) != nil {
		t.Error("a runner for an agent environment, which can't deploy stacks yet")
	}
}
