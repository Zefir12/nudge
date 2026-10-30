package hostload

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	stat = `cpu  100 20 30 800 50 0 0 0 0 0
cpu0 50 10 15 400 25 0 0 0 0 0
intr 12345
`
	meminfo = `MemTotal:        8000000 kB
MemFree:         1000000 kB
MemAvailable:    6000000 kB
Buffers:          100000 kB
`
)

// proc writes a fake proc dir with stat and meminfo.
func proc(t *testing.T, stat, meminfo string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"stat": stat, "meminfo": meminfo} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		stat, meminfo string
		want          Reading
		wantErr       bool
	}{
		{"ok", stat, meminfo, Reading{CPUBusy: 150, CPUTotal: 1000, MemoryTotal: 8000000 * 1024, MemoryAvailable: 6000000 * 1024}, false},
		{"old kernel without steal", "cpu 100 20 30 800 50\n", meminfo, Reading{CPUBusy: 150, CPUTotal: 1000, MemoryTotal: 8000000 * 1024, MemoryAvailable: 6000000 * 1024}, false},
		{"no cpu line", "intr 1\n", meminfo, Reading{}, true},
		{"short cpu line", "cpu 1 2\n", meminfo, Reading{}, true},
		{"bad number", "cpu 1 x 3 4 5\n", meminfo, Reading{}, true},
		{"no MemAvailable", stat, "MemTotal: 100 kB\n", Reading{}, true},
		{"bad memory", stat, "MemTotal: lots kB\nMemAvailable: 1 kB\n", Reading{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(strings.NewReader(tt.stat), strings.NewReader(tt.meminfo))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Parse = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCPUPercent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		prev, cur Reading
		want      float64
		wantOK    bool
	}{
		{"quarter busy", Reading{CPUBusy: 100, CPUTotal: 1000}, Reading{CPUBusy: 200, CPUTotal: 1400}, 25, true},
		{"idle", Reading{CPUBusy: 100, CPUTotal: 1000}, Reading{CPUBusy: 100, CPUTotal: 1400}, 0, true},
		{"same read", Reading{CPUBusy: 100, CPUTotal: 1000}, Reading{CPUBusy: 100, CPUTotal: 1000}, 0, false},
		{"rebooted", Reading{CPUBusy: 100, CPUTotal: 1000}, Reading{CPUBusy: 5, CPUTotal: 50}, 0, false},
		{"first read", Reading{}, Reading{CPUBusy: 100, CPUTotal: 1000}, 10, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := CPUPercent(tt.prev, tt.cur)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("CPUPercent = %v, %v; want %v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestMemoryUsed(t *testing.T) {
	t.Parallel()
	if got := (Reading{MemoryTotal: 10, MemoryAvailable: 4}).MemoryUsed(); got != 6 {
		t.Errorf("MemoryUsed = %d, want 6", got)
	}
	if got := (Reading{MemoryTotal: 4, MemoryAvailable: 10}).MemoryUsed(); got != 0 {
		t.Errorf("MemoryUsed with more available than total = %d, want 0", got)
	}
}

func TestRead(t *testing.T) {
	t.Parallel()
	if _, err := Read(t.TempDir()); !errors.Is(err, ErrUnknown) {
		t.Errorf("Read of an empty dir: err = %v, want ErrUnknown", err)
	}
	if _, err := Read(proc(t, "garbage\n", meminfo)); !errors.Is(err, ErrUnknown) {
		t.Errorf("Read of a bad stat: err = %v, want ErrUnknown", err)
	}
	r, err := Read(proc(t, stat, meminfo))
	if err != nil {
		t.Fatal(err)
	}
	if r.CPUTotal != 1000 || r.MemoryUsed() != 2000000*1024 {
		t.Errorf("Read = %+v", r)
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
	tests := []struct {
		name    string
		h       http.Handler
		wantErr error
	}{
		{"old agent", notFound, ErrUnknown},
		{"no proc", Control(t.TempDir(), notFound), ErrUnknown},
		{"ok", Control(proc(t, stat, meminfo), notFound), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, err := Remote(t.Context(), serve(t, tt.h))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Remote: err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && (r.CPUTotal != 1000 || r.MemoryTotal != 8000000*1024) {
				t.Errorf("Remote = %+v", r)
			}
		})
	}
}

// Control passes every other request on.
func TestControlPassesOn(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	dial := serve(t, Control(t.TempDir(), next))
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
	}}
	resp, err := hc.Get("http://agent/v1/disk")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want next's", resp.StatusCode)
	}
}
