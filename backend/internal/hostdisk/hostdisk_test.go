package hostdisk

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestStat(t *testing.T) {
	t.Parallel()
	if _, err := Stat(""); !errors.Is(err, ErrUnknown) {
		t.Errorf("Stat(\"\"): err = %v, want ErrUnknown", err)
	}
	if _, err := Stat(t.TempDir() + "/missing"); !errors.Is(err, ErrUnknown) {
		t.Errorf("Stat of a missing dir: err = %v, want ErrUnknown", err)
	}
	if runtime.GOOS != "linux" {
		return
	}
	sp, err := Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if sp.Total == 0 || sp.Free > sp.Total {
		t.Errorf("Stat = %+v, want a size and free space within it", sp)
	}
}

// serve dials an httptest server running h.
func serve(t *testing.T, h http.Handler) func(context.Context) (net.Conn, error) {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}
}

func TestRemote(t *testing.T) {
	t.Parallel()
	notFound := http.NotFoundHandler()
	// Without statfs a mounted root is unknown too.
	var mounted error
	if runtime.GOOS != "linux" {
		mounted = ErrUnknown
	}
	tests := []struct {
		name    string
		h       http.Handler
		wantErr error
	}{
		{"old agent", notFound, ErrUnknown},
		{"no mount", Control("", notFound), ErrUnknown},
		{"missing dir", Control("/nonexistent/docker", notFound), ErrUnknown},
		{"mounted", Control(t.TempDir(), notFound), mounted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sp, err := Remote(t.Context(), serve(t, tt.h))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Remote: err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && sp.Total == 0 {
				t.Errorf("Remote = %+v, want a size", sp)
			}
		})
	}
}

// Control passes every other request on.
func TestControlPassesOn(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	dial := serve(t, Control("", next))
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
	}}
	resp, err := hc.Get("http://agent/v1/stacks")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want next's", resp.StatusCode)
	}
}
