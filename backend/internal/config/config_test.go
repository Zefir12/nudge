package config

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
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
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90, UpdateCheckInterval: 6 * time.Hour},
		},
		{
			name: "env",
			env:  map[string]string{"NUDGE_ADDR": ":9000", "NUDGE_DATA": "/var/lib/nudge", "NUDGE_LOG_LEVEL": "debug", "NUDGE_ADMIN_PASSWORD": "s3cret-pw", "DOCKER_HOST": "tcp://10.0.0.2:2375"},
			want: Server{Addr: ":9000", DataDir: "/var/lib/nudge", StacksDir: "/var/lib/nudge/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelDebug, AdminPassword: "s3cret-pw", DockerHost: "tcp://10.0.0.2:2375", AuditRetentionDays: 90, UpdateCheckInterval: 6 * time.Hour},
		},
		{
			name: "flag wins over env",
			args: []string{"-addr", "127.0.0.1:1234"},
			env:  map[string]string{"NUDGE_ADDR": ":9000"},
			want: Server{Addr: "127.0.0.1:1234", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90, UpdateCheckInterval: 6 * time.Hour},
		},
		{name: "bad log level", args: []string{"-log-level", "loud"}, wantErr: true},
		{name: "empty addr", args: []string{"-addr", ""}, wantErr: true},
		{name: "empty docker host", args: []string{"-docker-host", ""}, wantErr: true},
		{name: "empty data dir", args: []string{"-data", ""}, wantErr: true},
		{name: "stray argument", args: []string{"extra"}, wantErr: true},
		{
			name: "tls and public url",
			env:  map[string]string{"NUDGE_TLS_CERT": "/certs/cert.pem", "NUDGE_TLS_KEY": "/certs/key.pem", "NUDGE_PUBLIC_URL": "https://nudge.example.net:8443/"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90, UpdateCheckInterval: 6 * time.Hour, TLSCert: "/certs/cert.pem", TLSKey: "/certs/key.pem", PublicURL: "https://nudge.example.net:8443"},
		},
		{
			name: "public url flag",
			args: []string{"-public-url", "http://10.0.0.5:8080"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90, UpdateCheckInterval: 6 * time.Hour, PublicURL: "http://10.0.0.5:8080"},
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
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 7, UpdateCheckInterval: 6 * time.Hour},
		},
		{
			name: "stacks",
			env:  map[string]string{"NUDGE_STACKS_DIR": "/opt/nudge/stacks", "NUDGE_COMPOSE": "/bin/compose"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/opt/nudge/stacks", Compose: "/bin/compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90, UpdateCheckInterval: 6 * time.Hour},
		},
		{name: "relative stacks dir", args: []string{"-stacks-dir", "stacks"}, wantErr: true},
		{name: "empty compose", args: []string{"-compose", ""}, wantErr: true},
		{name: "audit retention flag", args: []string{"-audit-retention-days", "0"}, wantErr: true},
		{
			name: "update check interval",
			env:  map[string]string{"NUDGE_UPDATE_CHECK_INTERVAL": "30m"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90, UpdateCheckInterval: 30 * time.Minute},
		},
		{
			name: "update checks off",
			args: []string{"-update-check-interval", "0"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90},
		},
		{name: "update check interval too short", args: []string{"-update-check-interval", "1m"}, wantErr: true},
		{name: "update check interval negative", args: []string{"-update-check-interval", "-1h"}, wantErr: true},
		{name: "update check interval not a duration", env: map[string]string{"NUDGE_UPDATE_CHECK_INTERVAL": "daily"}, wantErr: true},
		{name: "audit retention not a number", env: map[string]string{"NUDGE_AUDIT_RETENTION_DAYS": "forever"}, wantErr: true},
		{
			name: "trusted proxies",
			env:  map[string]string{"NUDGE_TRUSTED_PROXIES": " 172.16.0.0/12, 10.1.2.3 ,fd00::/8,::1,10.9.9.9/8,::ffff:192.0.2.1"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90, UpdateCheckInterval: 6 * time.Hour,
				TrustedProxies: []netip.Prefix{
					netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("10.1.2.3/32"), netip.MustParsePrefix("fd00::/8"),
					netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.0.2.1/32"),
				}},
		},
		{
			name: "trusted proxies flag wins over env",
			args: []string{"-trusted-proxies", "127.0.0.1"},
			env:  map[string]string{"NUDGE_TRUSTED_PROXIES": "10.0.0.0/8"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90, UpdateCheckInterval: 6 * time.Hour,
				TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}},
		},
		{name: "trusted proxies not an address", env: map[string]string{"NUDGE_TRUSTED_PROXIES": "proxy.local"}, wantErr: true},
		{name: "trusted proxies bad prefix length", args: []string{"-trusted-proxies", "10.0.0.0/33"}, wantErr: true},
		{name: "trusted proxies empty entry", args: []string{"-trusted-proxies", "10.0.0.1,,10.0.0.2"}, wantErr: true},
		{name: "trusted proxies with zone", args: []string{"-trusted-proxies", "fe80::1%eth0"}, wantErr: true},
		{name: "trusted proxies IPv4-mapped prefix shorter than /96", args: []string{"-trusted-proxies", "::ffff:0.0.0.0/80"}, wantErr: true},
		{
			name: "trusted proxies IPv4-mapped /96 is all of IPv4",
			args: []string{"-trusted-proxies", "::ffff:0.0.0.0/96"},
			want: Server{Addr: ":8080", DataDir: "/data", StacksDir: "/data/stacks", Compose: "/usr/local/bin/docker-compose", LogLevel: slog.LevelInfo, DockerHost: "unix:///var/run/docker.sock", AuditRetentionDays: 90, UpdateCheckInterval: 6 * time.Hour,
				TrustedProxies: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(k string) string { return tt.env[k] }
			got, err := LoadServer(tt.args, getenv, io.Discard)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
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
		stacksDir  string
		compose    string
	}
	def := want{server: "https://nudge.example.net", dataDir: "/data", level: slog.LevelInfo, dockerHost: "unix:///var/run/docker.sock", stacksDir: "/data/stacks", compose: "/usr/local/bin/docker-compose"}
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
			want: want{server: "http://10.0.0.1:8080/nudge", joinToken: "nudge_join_x", dataDir: "/var/lib/agent", insecure: true, level: slog.LevelDebug, dockerHost: "unix:///run/docker.sock", stacksDir: "/var/lib/agent/stacks", compose: "/usr/local/bin/docker-compose"},
		},
		{
			name: "flag wins over env",
			args: []string{"-server", "https://b.example.net", "-insecure=false"},
			env:  map[string]string{"NUDGE_SERVER_URL": "http://a.example.net", "NUDGE_INSECURE": "1"},
			want: want{server: "https://b.example.net", dataDir: "/data", level: slog.LevelInfo, dockerHost: "unix:///var/run/docker.sock", stacksDir: "/data/stacks", compose: "/usr/local/bin/docker-compose"},
		},
		{name: "bare insecure flag", args: []string{"-insecure", "-server", "http://a.example.net"}, want: want{server: "http://a.example.net", insecure: true, dataDir: "/data", level: slog.LevelInfo, dockerHost: "unix:///var/run/docker.sock", stacksDir: "/data/stacks", compose: "/usr/local/bin/docker-compose"}},
		{
			name: "stacks",
			args: []string{"-server", "https://nudge.example.net"},
			env:  map[string]string{"NUDGE_STACKS_DIR": "/opt/nudge/stacks", "NUDGE_COMPOSE": "/bin/compose"},
			want: want{server: "https://nudge.example.net", dataDir: "/data", level: slog.LevelInfo, dockerHost: "unix:///var/run/docker.sock", stacksDir: "/opt/nudge/stacks", compose: "/bin/compose"},
		},
		{name: "relative stacks dir", args: []string{"-server", "https://a.example.net", "-stacks-dir", "stacks"}, wantErr: "absolute path"},
		{name: "empty compose", args: []string{"-server", "https://a.example.net", "-compose", ""}, wantErr: "compose binary"},
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
			g := want{server: got.ServerURL.String(), joinToken: got.JoinToken, dataDir: got.DataDir, insecure: got.Insecure, level: got.LogLevel, dockerHost: got.DockerHost, stacksDir: got.StacksDir, compose: got.Compose}
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
