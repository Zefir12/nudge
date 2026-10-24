package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

// muxed frames chunks like the daemon does for a container without a TTY.
func muxed(chunks ...string) []byte {
	var b []byte
	for i := 0; i+1 < len(chunks); i += 2 {
		stream := byte(1)
		if chunks[i] == "stderr" {
			stream = 2
		}
		header := make([]byte, 8)
		header[0] = stream
		binary.BigEndian.PutUint32(header[4:], uint32(len(chunks[i+1])))
		b = append(append(b, header...), chunks[i+1]...)
	}
	return b
}

type logsTest struct {
	envTest
	srv  *httptest.Server
	fake *dockertest.Fake
}

func newLogsTest(t *testing.T) logsTest {
	t.Helper()
	fake := &dockertest.Fake{Resources: dockertest.Resources{
		ContainerDetails: map[string]container.InspectResponse{
			"web":  {ID: "abc123", Config: &container.Config{}},
			"term": {ID: "def456", Config: &container.Config{Tty: true}},
			"long": {ID: "fed789", Config: &container.Config{Tty: true}},
		},
		Logs: map[string][]byte{
			"web": muxed(
				"stdout", "2026-10-01T12:00:00.5Z starting\n2026-10-01T12:00:01Z rea",
				"stdout", "dy\n",
				"stderr", "2026-10-01T12:00:02Z warn: slow\n",
				"stdout", "no newline",
			),
			"term": []byte("$ ls\r\nfile\r\n"),
			"long": []byte(strings.Repeat("x", maxLogLine+10)),
		},
		LogsCancelled: make(chan string, 1),
	}}
	et := newEnvTest(t, map[string]*dockertest.Fake{
		"local":  fake,
		"down":   {Resources: dockertest.Resources{ResourceErr: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}},
		"broken": {Resources: dockertest.Resources{ContainerDetails: map[string]container.InspectResponse{"web": {}}, LogsErr: errors.New("daemon: secret-detail")}},
	})
	srv := httptest.NewServer(et.h)
	t.Cleanup(srv.Close)
	return logsTest{envTest: et, srv: srv, fake: fake}
}

func (lt logsTest) path(env, query string) string {
	return "/api/v1/environments/" + strconv.FormatInt(lt.ids[env], 10) + "/containers/" + query
}

// dial opens a log stream with the session cookie.
func (lt logsTest) dial(ctx context.Context, t *testing.T, path string) (*websocket.Conn, error) {
	t.Helper()
	h := http.Header{}
	h.Set("Cookie", lt.cookie.String())
	ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(lt.srv.URL, "http")+path, &websocket.DialOptions{HTTPHeader: h})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return ws, err
}

// readAll reads frames until the server closes the socket.
func readAll(ctx context.Context, t *testing.T, ws *websocket.Conn) []map[string]any {
	t.Helper()
	var frames []map[string]any
	for {
		_, data, err := ws.Read(ctx)
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
			return frames
		}
		if err != nil {
			t.Fatalf("read: %v (after %v)", err, frames)
		}
		var f map[string]any
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
	}
}

func TestContainerLogsStream(t *testing.T) {
	t.Parallel()
	lt := newLogsTest(t)
	tests := []struct {
		name  string
		query string
		want  string
		opts  client.ContainerLogsOptions
	}{
		{
			name:  "demultiplexed with timestamps",
			query: "web/logs?timestamps=true&tail=100",
			want: `[{"line":"starting","stream":"stdout","time":"2026-10-01T12:00:00.5Z"},` +
				`{"line":"ready","stream":"stdout","time":"2026-10-01T12:00:01Z"},` +
				`{"line":"warn: slow","stream":"stderr","time":"2026-10-01T12:00:02Z"},` +
				`{"line":"no newline","stream":"stdout"},{"done":true}]`,
			opts: client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Timestamps: true, Tail: "100"},
		},
		{
			name:  "without timestamps the prefix is part of the line",
			query: "web/logs",
			want: `[{"line":"2026-10-01T12:00:00.5Z starting","stream":"stdout"},` +
				`{"line":"2026-10-01T12:00:01Z ready","stream":"stdout"},` +
				`{"line":"2026-10-01T12:00:02Z warn: slow","stream":"stderr"},` +
				`{"line":"no newline","stream":"stdout"},{"done":true}]`,
			opts: client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "all"},
		},
		{
			name:  "tty stream is raw stdout",
			query: "term/logs?tail=all",
			want:  `[{"line":"$ ls","stream":"stdout"},{"line":"file","stream":"stdout"},{"done":true}]`,
			opts:  client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "all"},
		},
		{
			name:  "long line is split",
			query: "long/logs",
			want:  `[{"line":"` + strings.Repeat("x", maxLogLine) + `","stream":"stdout"},{"line":"xxxxxxxxxx","stream":"stdout"},{"done":true}]`,
			opts:  client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "all"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ws, err := lt.dial(ctx, t, lt.path("local", tt.query))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ws.CloseNow() }()
			got, _ := json.Marshal(readAll(ctx, t, ws))
			if string(got) != tt.want {
				t.Errorf("frames =\n%s\nwant\n%s", got, tt.want)
			}
			found := false
			for _, o := range lt.fake.LogOptions() {
				found = found || o == tt.opts
			}
			if !found {
				t.Errorf("options %+v not in %+v", tt.opts, lt.fake.LogOptions())
			}
		})
	}
}

func TestContainerLogsStreamError(t *testing.T) {
	t.Parallel()
	lt := newLogsTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, err := lt.dial(ctx, t, lt.path("broken", "web/logs"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	got, _ := json.Marshal(readAll(ctx, t, ws))
	if want := `[{"error":"the environment's Docker host ended the log stream with an error"}]`; string(got) != want {
		t.Errorf("frames = %s, want %s", got, want)
	}
}

// Closing the socket (the browser tab) must cancel the Docker stream, which a followed
// stream would otherwise keep open forever.
func TestContainerLogsCloseCancelsStream(t *testing.T) {
	t.Parallel()
	lt := newLogsTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, err := lt.dial(ctx, t, lt.path("local", "term/logs?follow=true"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err := ws.Read(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := ws.Close(websocket.StatusNormalClosure, "tab closed"); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-lt.fake.LogsCancelled:
		if id != "term" {
			t.Errorf("cancelled %q", id)
		}
	case <-ctx.Done():
		t.Fatal("the Docker stream was not cancelled")
	}
}

func TestContainerLogsRefused(t *testing.T) {
	t.Parallel()
	lt := newLogsTest(t)
	tests := []struct {
		name    string
		path    string
		cookie  bool
		headers map[string]string
		code    int
		want    string
	}{
		{name: "no session", path: lt.path("local", "web/logs"), code: http.StatusUnauthorized, want: "authentication required"},
		{name: "unknown container", path: lt.path("local", "nope/logs"), cookie: true, code: http.StatusNotFound, want: "container not found"},
		{name: "bad id", path: lt.path("local", "..x/logs"), cookie: true, code: http.StatusNotFound, want: "container not found"},
		{name: "bad tail", path: lt.path("local", "web/logs?tail=-1"), cookie: true, code: http.StatusBadRequest, want: "tail must be a non-negative integer or all"},
		{name: "bad follow", path: lt.path("local", "web/logs?follow=maybe"), cookie: true, code: http.StatusBadRequest, want: "follow must be true or false"},
		{name: "offline", path: lt.path("down", "web/logs"), cookie: true, code: http.StatusServiceUnavailable, want: "the environment is offline"},
		{name: "unknown environment", path: "/api/v1/environments/999/containers/web/logs", cookie: true, code: http.StatusNotFound, want: "environment not found"},
		{
			name: "cross-origin upgrade", path: lt.path("local", "web/logs"), cookie: true, code: http.StatusForbidden,
			headers: map[string]string{
				"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13",
				"Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==", "Origin": "https://evil.example",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := request{method: "GET", path: tt.path, headers: tt.headers}
			if tt.cookie {
				req.cookie = lt.cookie
			}
			rec := do(t, lt.h, req)
			if rec.Code != tt.code {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tt.code, rec.Body)
			}
			if tt.want != "" && !strings.Contains(rec.Body.String(), `"error":"`+tt.want+`"`) {
				t.Errorf("body = %s, want error %q", rec.Body, tt.want)
			}
			if strings.Contains(rec.Body.String(), "secret-detail") {
				t.Errorf("body leaks the daemon's message: %s", rec.Body)
			}
		})
	}
}
