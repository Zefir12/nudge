package config

import (
	"io"
	"log/slog"
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
			want: Server{Addr: ":8080", DataDir: "/data", LogLevel: slog.LevelInfo},
		},
		{
			name: "env",
			env:  map[string]string{"NUDGE_ADDR": ":9000", "NUDGE_DATA": "/var/lib/nudge", "NUDGE_LOG_LEVEL": "debug", "NUDGE_ADMIN_PASSWORD": "s3cret-pw"},
			want: Server{Addr: ":9000", DataDir: "/var/lib/nudge", LogLevel: slog.LevelDebug, AdminPassword: "s3cret-pw"},
		},
		{
			name: "flag wins over env",
			args: []string{"-addr", "127.0.0.1:1234"},
			env:  map[string]string{"NUDGE_ADDR": ":9000"},
			want: Server{Addr: "127.0.0.1:1234", DataDir: "/data", LogLevel: slog.LevelInfo},
		},
		{name: "bad log level", args: []string{"-log-level", "loud"}, wantErr: true},
		{name: "empty addr", args: []string{"-addr", ""}, wantErr: true},
		{name: "empty data dir", args: []string{"-data", ""}, wantErr: true},
		{name: "stray argument", args: []string{"extra"}, wantErr: true},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: true},
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
