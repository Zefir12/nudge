package config

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestLoadServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		want    Server
		wantErr bool
	}{
		{
			name: "defaults",
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90},
		},
		{
			name: "env",
			env:  map[string]string{"NUDGE_ADDR": ":9000", "NUDGE_DATA": "/var/lib/nudge", "NUDGE_LOG_LEVEL": "debug", "NUDGE_ADMIN_PASSWORD": "s3cret-pw", "DOCKER_HOST": "tcp://10.0.0.2:2375"},
			want: Server{Addr: ":9000", DataDir: "/var/lib/nudge", StacksDir: "/var/lib/nudge/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelDebug, AdminPassword: "s3cret-pw", DockerHost: "tcp://10.0.0.2:2375", AuditRetentionDays: 90},
		},
		{
			name: "flag wins over env",
			args: []string{"-addr", "127.0.0.1:1234"},
			env:  map[string]string{"NUDGE_ADDR": ":9000"},
			want: Server{Addr: "127.0.0.1:1234", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90},
		},
		{name: "bad log level", args: []string{"-log-level", "loud"}, wantErr: true},
		{name: "empty addr", args: []string{"-addr", ""}, wantErr: true},
		{name: "empty docker host", args: []string{"-docker-host", ""}, wantErr: true},
		{name: "empty data dir", args: []string{"-data", ""}, wantErr: true},
		{name: "stray argument", args: []string{"extra"}, wantErr: true},
		{
			name: "tls and public url",
			env:  map[string]string{"NUDGE_TLS_CERT": "/certs/cert.pem", "NUDGE_TLS_KEY": "/certs/key.pem", "NUDGE_PUBLIC_URL": "https://nudge.example.net:8443/"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90, TLSCert: "/certs/cert.pem", TLSKey: "/certs/key.pem", PublicURL: "https://nudge.example.net:8443"},
		},
		{
			name: "public url flag",
			args: []string{"-public-url", "http://10.0.0.5:8080"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90, PublicURL: "http://10.0.0.5:8080"},
		},
		{name: "cert without key", args: []string{"-tls-cert", "/c.pem"}, wantErr: true},
		{name: "key without cert", env: map[string]string{"NUDGE_TLS_KEY": "/k.pem"}, wantErr: true},
		{name: "public url without scheme", args: []string{"-public-url", "nudge.example.net"}, wantErr: true},
		{name: "public url other scheme", args: []string{"-public-url", "ftp://nudge.example.net"}, wantErr: true},
		{name: "public url with path", args: []string{"-public-url", "https://example.net/nudge"}, wantErr: true},
		{name: "public url with query", args: []string{"-public-url", "https://example.net/?a=b"}, wantErr: true},
		{name: "public url with credentials", args: []string{"-public-url", "https://u:p@example.net"}, wantErr: true},
		{name: "public url with shell characters", args: []string{"-public-url", "https://a;b"}, wantErr: true},
		{name: "public url without host", args: []string{"-public-url", "https://"}, wantErr: true},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: true},
		{
			name: "audit retention",
			env:  map[string]string{"NUDGE_AUDIT_RETENTION_DAYS": "7"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 7},
		},
		{
			name: "stacks",
			env:  map[string]string{"NUDGE_STACKS_DIR": "/opt/nudge/stacks", "NUDGE_COMPOSE": "/bin/compose"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/opt/nudge/stacks", Compose: "/bin/compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90},
		},
		{name: "relative stacks dir", args: []string{"-stacks-dir", "stacks"}, wantErr: true},
		{name: "empty compose", args: []string{"-compose", ""}, wantErr: true},
		{name: "audit retention flag", args: []string{"-audit-retention-days", "0"}, wantErr: true},
		{name: "audit retention not a number", env: map[string]string{"NUDGE_AUDIT_RETENTION_DAYS": "forever"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(k string) string { return tt.env[k] }
			got, err := LoadServer(tt.args, getenv, io.Discard)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadAgent(t *testing.T) {
	t.Parallel()
	type want struct {
		server     string
		joinToken  string
		dataDir    string
		insecure   bool
		level      slog.Level
		dockerHost string
	}
	def := want{server: "https://nudge.example.net", dataDir: "/data", level: slog.LevelInfo, dockerHost: "unix:///var/run/docker.sock"}
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		want    want
		wantErr string
	}{
		{name: "flags", args: []string{"-server", "https://nudge.example.net"}, want: def},
		{
			name: "env",
			env: map[string]string{
				"NUDGE_SERVER_URL": "http://10.0.0.1:8080/nudge/", "NUDGE_JOIN_TOKEN": "nudge_join_x", "NUDGE_DATA": "/var/lib/agent",
				"NUDGE_INSECURE": "true", "NUDGE_LOG_LEVEL": "debug", "DOCKER_HOST": "unix:///run/docker.sock",
			},
			want: want{server: "http://10.0.0.1:8080/nudge", joinToken: "nudge_join_x", dataDir: "/var/lib/agent", insecure: true, level: slog.LevelDebug, dockerHost: "unix:///run/docker.sock"},
		},
		{
			name: "flag wins over env",
			args: []string{"-server", "https://b.example.net", "-insecure=false"},
			env:  map[string]string{"NUDGE_SERVER_URL": "http://a.example.net", "NUDGE_INSECURE": "1"},
			want: want{server: "https://b.example.net", dataDir: "/data", level: slog.LevelInfo, dockerHost: "unix:///var/run/docker.sock"},
		},
		{name: "bare insecure flag", args: []string{"-insecure", "-server", "http://a.example.net"}, want: want{server: "http://a.example.net", insecure: true, dataDir: "/data", level: slog.LevelInfo, dockerHost: "unix:///var/run/docker.sock"}},
		{name: "missing server", wantErr: "server URL is required"},
		{name: "http without insecure", args: []string{"-server", "http://a.example.net"}, wantErr: "-insecure"},
		{name: "other scheme", args: []string{"-server", "ftp://a.example.net"}, wantErr: "want https://"},
		{name: "no scheme", args: []string{"-server", "a.example.net"}, wantErr: "want https://"},
		{name: "no host", args: []string{"-server", "https:///x"}, wantErr: "missing host"},
		{name: "credentials in URL", args: []string{"-server", "https://u:p@a.example.net"}, wantErr: "only scheme"},
		{name: "query", args: []string{"-server", "https://a.example.net/?x=1"}, wantErr: "only scheme"},
		{name: "bad insecure env", args: []string{"-server", "https://a.example.net"}, env: map[string]string{"NUDGE_INSECURE": "maybe"}, wantErr: "NUDGE_INSECURE"},
		{name: "bad log level", args: []string{"-server", "https://a.example.net", "-log-level", "loud"}, wantErr: "log level"},
		{name: "empty docker host", args: []string{"-server", "https://a.example.net", "-docker-host", ""}, wantErr: "docker host"},
		{name: "empty data dir", args: []string{"-server", "https://a.example.net", "-data", ""}, wantErr: "data directory"},
		{name: "stray argument", args: []string{"-server", "https://a.example.net", "extra"}, wantErr: "unexpected arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(k string) string { return tt.env[k] }
			got, err := LoadAgent(tt.args, getenv, io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			g := want{server: got.ServerURL.String(), joinToken: got.JoinToken, dataDir: got.DataDir, insecure: got.Insecure, level: got.LogLevel, dockerHost: got.DockerHost}
			if g != tt.want {
				t.Errorf("got %+v, want %+v", g, tt.want)
			}
		})
	}
}

func TestLoadAgentHelpHidesJoinToken(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	getenv := func(k string) string {
		return map[string]string{"NUDGE_JOIN_TOKEN": "nudge_join_s3cret"}[k]
	}
	if _, err := LoadAgent([]string{"-h"}, getenv, &out); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Errorf("usage shows the join token:\n%s", out.String())
	}
}
