package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/config"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
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

func TestWarnTrustedProxies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		proxies string
		warn    bool
	}{
		{name: "none", proxies: ""},
		{name: "narrow", proxies: "172.16.0.0/12,fd00::/8,10.0.0.1/32"},
		{name: "every IPv4 address", proxies: "10.0.0.0/8,0.0.0.0/0", warn: true},
		{name: "every IPv6 address", proxies: "::/0", warn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var p []netip.Prefix
			for s := range strings.SplitSeq(tt.proxies, ",") {
				if s != "" {
					p = append(p, netip.MustParsePrefix(s))
				}
			}
			var buf bytes.Buffer
			warnTrustedProxies(slog.New(slog.NewTextHandler(&buf, nil)), p)
			if got := strings.Contains(buf.String(), "level=WARN"); got != tt.warn {
				t.Errorf("warned = %v, want %v (%s)", got, tt.warn, buf.String())
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
	if strings.Contains(stderr.String(), "nudge-agent") {
		t.Errorf("stderr = %q, want no warning to switch to the nudge-agent image", stderr.String())
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
	runner := stackRunner(config.Server{StacksDir: t.TempDir(), Compose: "/bin/compose", DockerHost: "unix:///docker.sock"}, nil, nil, slog.New(slog.DiscardHandler))
	if _, ok := runner(environments.Environment{ID: 1, Kind: environments.KindLocal}).(*stacks.Local); !ok {
		t.Error("the local environment's runner isn't a stacks.Local")
	}
}

func TestStackCredentials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	regs := registries.New(registries.Options{DB: st.DB(), Box: box})
	pw := "gh-pass"
	if _, err := regs.Create(ctx, registries.Input{Name: "GitHub", Address: "ghcr.io", Username: "bot", Password: &pw}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		files stacks.Files
		want  []stacks.Credential
	}{
		{"named in compose", stacks.Files{Compose: "services:\n  a:\n    image: ghcr.io/org/a\n"},
			[]stacks.Credential{{Address: "ghcr.io", Username: "bot", Password: "gh-pass"}}},
		{"named in .env", stacks.Files{Compose: "services: {}\n", Env: "IMAGE=ghcr.io/org/a\n"},
			[]stacks.Credential{{Address: "ghcr.io", Username: "bot", Password: "gh-pass"}}},
		{"not named", stacks.Files{Compose: "services:\n  a:\n    image: nginx\n"}, []stacks.Credential{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := stackCredentials(regs)(ctx, tt.files)
			if err != nil || !slices.Equal(got, tt.want) {
				t.Errorf("credentials = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}
