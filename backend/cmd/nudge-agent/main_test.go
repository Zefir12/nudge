package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
		{name: "help", args: []string{"help"}, wantCode: 0, wantStdout: "usage: nudge-agent"},
		{name: "default command is agent", args: []string{"-h"}, wantCode: 0, wantStderr: "-join-token"},
		{name: "agent help", args: []string{"agent", "-h"}, wantCode: 0, wantStderr: "-join-token"},
		{name: "without server", args: nil, wantCode: 2, wantStderr: "server URL is required"},
		{name: "refuses http", args: []string{"-server", "http://nudge.example.net"}, wantCode: 2, wantStderr: "-insecure"},
		{name: "probe usage", args: []string{"probe"}, wantCode: 2, wantStderr: "usage: nudge probe"},
		{name: "serve is not here", args: []string{"serve"}, wantCode: 2, wantStderr: `unknown command "serve"`},
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

func TestAgentWithoutCredentials(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	getenv := func(string) string { return "" }
	args := []string{"-server", "http://127.0.0.1:1", "-insecure", "-docker-host", "unix://" + filepath.Join(t.TempDir(), "none.sock"), "-data", t.TempDir()}
	if code := realMain(context.Background(), args, getenv, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, want 1 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no join token") {
		t.Errorf("stderr = %q, want the missing join token explained", stderr.String())
	}
}

// TestServerCodeLeftOut keeps the agent binary free of the server's packages: the
// point of its own image is a smaller binary and attack surface on agent hosts.
func TestServerCodeLeftOut(t *testing.T) {
	t.Parallel()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command to list the dependencies")
	}
	out, err := exec.CommandContext(t.Context(), gobin, "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	const internal = "git.zefirlabs.net/zefir/nudge/backend/internal/"
	allowed := map[string]bool{
		"agent": true, "cli": true, "config": true, "docker": true, "hostdisk": true, "probe": true,
		"selfupdate": true, "stacks": true, "tunnel": true, "version": true,
	}
	for pkg := range strings.FieldsSeq(string(out)) {
		name, ok := strings.CutPrefix(pkg, internal)
		if ok && !allowed[name] {
			t.Errorf("nudge-agent imports %s", pkg)
		}
		for _, server := range []string{"modernc.org/sqlite", "github.com/coreos/go-oidc", "golang.org/x/crypto/argon2"} {
			if strings.HasPrefix(pkg, server) {
				t.Errorf("nudge-agent imports %s", pkg)
			}
		}
	}
}
