package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/moby/moby/api/types/container"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func TestStreamLimiter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		max   int
		admin bool
		want  int
	}{
		{name: "user", max: 2, want: 2},
		{name: "admin", max: 2, admin: true, want: 2 * adminStreamFactor},
		{name: "default", max: 0, want: defaultMaxStreams},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := newStreamLimiter(tt.max)
			var releases []func()
			for range tt.want {
				release, ok := l.acquire(1, 1, tt.admin)
				if !ok {
					t.Fatalf("refused after %d streams, want %d", len(releases), tt.want)
				}
				releases = append(releases, release)
			}
			if _, ok := l.acquire(1, 1, tt.admin); ok {
				t.Fatalf("stream %d was allowed", tt.want+1)
			}
			// Other environments and other users have their own slots.
			for _, k := range []streamKey{{1, 2}, {2, 1}} {
				release, ok := l.acquire(k.user, k.env, tt.admin)
				if !ok {
					t.Fatalf("%+v refused", k)
				}
				release()
			}
			releases[0]()
			releases[0]() // twice gives back one slot, not two
			release, ok := l.acquire(1, 1, tt.admin)
			if !ok {
				t.Fatal("a released slot was not reusable")
			}
			if _, ok := l.acquire(1, 1, tt.admin); ok {
				t.Fatal("a double release freed two slots")
			}
			release()
			for _, r := range releases[1:] {
				r()
			}
			l.mu.Lock()
			defer l.mu.Unlock()
			if len(l.open) != 0 {
				t.Errorf("map keeps %d keys with no stream open", len(l.open))
			}
		})
	}
}

// Followed log streams hold their slots; over the bound, logs, stats, exec and the stats
// overview answer 429, and a closed stream frees its slot.
func TestStreamLimitHandlers(t *testing.T) {
	t.Parallel()
	running := &container.State{Running: true}
	fake := &dockertest.Fake{Resources: dockertest.Resources{
		ContainerDetails: map[string]container.InspectResponse{
			"term": {ID: "def456", Config: &container.Config{Tty: true}, State: running},
		},
		Logs:          map[string][]byte{"term": []byte("$ ls\r\n")},
		LogsCancelled: make(chan string, 8),
	}}
	et := newEnvTestOpts(t, map[string]*dockertest.Fake{"local": fake}, func(o *Options) { o.MaxStreams = 1 })
	srv := httptest.NewServer(et.h)
	t.Cleanup(srv.Close)
	base := "/api/v1/environments/" + strconv.FormatInt(et.ids["local"], 10)
	logs := base + "/containers/term/logs?follow=true"

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	dial := func() (*websocket.Conn, error) {
		h := http.Header{}
		h.Set("Cookie", et.cookie.String())
		ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+logs, &websocket.DialOptions{HTTPHeader: h})
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return ws, err
	}
	// The session is an admin's: four times MaxStreams.
	var open []*websocket.Conn
	for range adminStreamFactor {
		ws, err := dial()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := ws.Read(ctx); err != nil {
			t.Fatal(err)
		}
		open = append(open, ws)
	}
	defer func() {
		for _, ws := range open {
			_ = ws.CloseNow()
		}
	}()

	for _, path := range []string{
		logs,
		base + "/containers/term/stats",
		base + "/containers/term/exec",
		base + "/stats",
	} {
		rec := do(t, et.h, request{method: "GET", path: path, cookie: et.cookie})
		if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), errTooManyStreams) {
			t.Errorf("%s: %d %s, want 429", path, rec.Code, rec.Body)
		}
	}

	if err := open[0].Close(websocket.StatusNormalClosure, "done"); err != nil {
		t.Fatal(err)
	}
	open = open[1:]
	for {
		ws, err := dial()
		if err == nil {
			open = append(open, ws)
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("the closed stream's slot was not freed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
