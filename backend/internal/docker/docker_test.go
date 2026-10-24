package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/moby/moby/client"
)

func TestLocalDialer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		host    string
		wantErr bool
	}{
		{host: "unix:///var/run/docker.sock"},
		{host: "tcp://127.0.0.1:2375"},
		{host: "ssh://user@host", wantErr: true},
		{host: "npipe:////./pipe/docker_engine", wantErr: true},
		{host: "/var/run/docker.sock", wantErr: true},
		{host: "unix://", wantErr: true},
		{host: "tcp://", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			_, err := LocalDialer(tt.host)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// fakeDaemon serves handler on a unix socket and returns its unix:// host.
func fakeDaemon(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return "unix://" + sock
}

func TestClientOverDialer(t *testing.T) {
	t.Parallel()
	host := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.51")
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "_ping":
			_, _ = fmt.Fprint(w, "OK")
		case "version":
			_, _ = fmt.Fprint(w, `{"Version":"28.5.1","ApiVersion":"1.51","Os":"linux","Arch":"amd64"}`)
		default:
			http.Error(w, `{"message":"no such thing"}`, http.StatusNotFound)
		}
	})
	dial, err := LocalDialer(host)
	if err != nil {
		t.Fatal(err)
	}
	// Count dials to show that every connection goes through dial.
	var dials atomic.Int32
	c, err := New(func(ctx context.Context) (net.Conn, error) {
		dials.Add(1)
		return dial(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if _, err := c.Ping(ctx, client.PingOptions{}); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	v, err := c.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		t.Fatalf("ServerVersion: %v", err)
	}
	if v.Version != "28.5.1" || v.Os != "linux" {
		t.Errorf("version = %+v", v)
	}
	if dials.Load() == 0 {
		t.Error("client did not use the dialer")
	}
	var _ Client = c
}

func TestUnreachable(t *testing.T) {
	t.Parallel()
	dial, err := LocalDialer("unix://" + filepath.Join(t.TempDir(), "missing.sock"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_, err = c.Ping(context.Background(), client.PingOptions{})
	if err == nil || !Unreachable(err) {
		t.Errorf("Ping on a missing socket: err = %v, want Unreachable", err)
	}
	if Unreachable(errors.New("daemon said no")) {
		t.Error("a plain error is not Unreachable")
	}
}
