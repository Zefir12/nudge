package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

type execTest struct {
	logsTest
}

func newExecTest(t *testing.T) execTest {
	t.Helper()
	running := &container.State{Running: true}
	fake := &dockertest.Fake{
		Resources: dockertest.Resources{
			ContainerDetails: map[string]container.InspectResponse{
				"web":     {ID: "abc123", State: running},
				"stopped": {ID: "def456", State: &container.State{Status: container.StateExited}},
			},
			Execs:        make(chan *dockertest.Exec, 1),
			ExecExitCode: 3,
		},
	}
	et := newEnvTest(t, map[string]*dockertest.Fake{
		"local": fake,
		"down":  {Resources: dockertest.Resources{ResourceErr: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}},
		"broken": {Resources: dockertest.Resources{
			ContainerDetails: map[string]container.InspectResponse{"web": {State: running}},
			ExecErr:          errors.New("daemon: secret-detail"),
		}},
	})
	srv := httptest.NewServer(et.h)
	t.Cleanup(srv.Close)
	return execTest{logsTest{envTest: et, srv: srv, fake: fake}}
}

func (xt execTest) open(ctx context.Context, t *testing.T, query string) (*websocket.Conn, *dockertest.Exec) {
	t.Helper()
	ws, err := xt.dial(ctx, t, xt.path("local", "web/exec"+query))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	select {
	case e := <-xt.fake.Execs:
		t.Cleanup(func() { _ = e.Conn.Close() })
		return ws, e
	case <-ctx.Done():
		t.Fatal("no exec attached")
		return nil, nil
	}
}

// A session pipes input and output both ways, resizes the terminal and, when the
// process ends, sends its exit code and closes the socket.
func TestContainerExecSession(t *testing.T) {
	t.Parallel()
	xt := newExecTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, e := xt.open(ctx, t, "?cols=80&rows=24")

	want := client.ExecCreateOptions{
		TTY: true, ConsoleSize: client.ConsoleSize{Width: 80, Height: 24},
		AttachStdin: true, AttachStdout: true, AttachStderr: true,
		Env: []string{"TERM=xterm-256color"}, Cmd: []string{"/bin/sh"},
	}
	if e.ContainerID != "web" || fmt.Sprint(e.Options) != fmt.Sprint(want) {
		t.Errorf("exec on %q with %+v, want web with %+v", e.ContainerID, e.Options, want)
	}

	go func() { _, _ = e.Conn.Write([]byte("$ ")) }()
	typ, data, err := ws.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || string(data) != "$ " {
		t.Fatalf("read = %v %q %v, want binary %q", typ, data, err, "$ ")
	}

	if err := ws.Write(ctx, websocket.MessageBinary, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, err := e.Conn.Read(buf)
	if err != nil || string(buf[:n]) != "ls\n" {
		t.Fatalf("process read %q %v, want %q", buf[:n], err, "ls\n")
	}

	for _, msg := range []string{`{"type":"resize","cols":0,"rows":5}`, `nonsense`, `{"type":"resize","cols":120,"rows":40}`} {
		if err := ws.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			t.Fatal(err)
		}
	}
	for !slices.Equal(xt.fake.ExecResizes(), []client.ExecResizeOptions{{Width: 120, Height: 40}}) {
		if ctx.Err() != nil {
			t.Fatalf("resizes = %+v, want one to 120x40", xt.fake.ExecResizes())
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := e.Conn.Close(); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(readAll(ctx, t, ws))
	if want := `[{"done":true,"exit_code":3}]`; string(got) != want {
		t.Errorf("frames = %s, want %s", got, want)
	}

	entries := keys(auditEntries(t, xt.h, xt.cookie, "").Entries)
	wantEntry := entryKey{User: "admin", Environment: strconv.FormatInt(xt.ids["local"], 10),
		Action: "GET /api/v1/environments/{env}/containers/{id}/exec", Target: "web", Status: http.StatusSwitchingProtocols}
	if !slices.Contains(entries, wantEntry) {
		t.Errorf("audit = %+v, want %+v", entries, wantEntry)
	}
}

// Closing the socket ends the process's terminal.
func TestContainerExecClientCloses(t *testing.T) {
	t.Parallel()
	xt := newExecTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, e := xt.open(ctx, t, "?cmd="+url.QueryEscape(" bash  -l "))
	if want := []string{"bash", "-l"}; !slices.Equal(e.Options.Cmd, want) {
		t.Errorf("cmd = %q, want %q", e.Options.Cmd, want)
	}
	if err := ws.Close(websocket.StatusNormalClosure, "tab closed"); err != nil {
		t.Fatal(err)
	}
	// A pipe refuses a deadline once the server closed its end: what this waits for.
	_ = e.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := e.Conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("process read err = %v, want EOF", err)
	}
}

func TestContainerExecRefused(t *testing.T) {
	t.Parallel()
	xt := newExecTest(t)
	tests := []struct {
		name   string
		env    string
		path   string
		cookie bool
		code   int
		want   string
	}{
		{name: "no session", path: "web/exec", code: http.StatusUnauthorized, want: "authentication required"},
		{name: "unknown container", path: "nope/exec", cookie: true, code: http.StatusNotFound, want: "container not found"},
		{name: "bad id", path: "..x/exec", cookie: true, code: http.StatusNotFound, want: "container not found"},
		{name: "stopped", path: "stopped/exec", cookie: true, code: http.StatusConflict, want: "container is not running"},
		{name: "blank cmd", path: "web/exec?cmd=%20", cookie: true, code: http.StatusBadRequest, want: "cmd must be a command of at most 1024 characters"},
		{name: "long cmd", path: "web/exec?cmd=" + strings.Repeat("a", maxExecCmd+1), cookie: true, code: http.StatusBadRequest, want: "cmd must be a command of at most 1024 characters"},
		{name: "cols only", path: "web/exec?cols=80", cookie: true, code: http.StatusBadRequest, want: "cols and rows must both be between 1 and 1000"},
		{name: "too wide", path: "web/exec?cols=1001&rows=5", cookie: true, code: http.StatusBadRequest, want: "cols and rows must both be between 1 and 1000"},
		{name: "offline", env: "down", path: "web/exec", cookie: true, code: http.StatusServiceUnavailable, want: "the environment is offline"},
		{name: "daemon error", env: "broken", path: "web/exec", cookie: true, code: http.StatusBadGateway, want: "the environment's Docker host returned an error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := cmp.Or(tt.env, "local")
			req := request{method: "GET", path: xt.path(env, tt.path)}
			if tt.cookie {
				req.cookie = xt.cookie
			}
			rec := do(t, xt.h, req)
			if rec.Code != tt.code {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tt.code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), `"error":"`+tt.want+`"`) || strings.Contains(rec.Body.String(), "secret") {
				t.Errorf("body = %s, want error %q", rec.Body, tt.want)
			}
		})
	}
}

// A refused session is recorded with its status.
func TestContainerExecAuditsRefusal(t *testing.T) {
	t.Parallel()
	xt := newExecTest(t)
	rec := do(t, xt.h, request{method: "GET", path: xt.path("local", "stopped/exec"), cookie: xt.cookie})
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d", rec.Code)
	}
	want := []entryKey{{User: "admin", Environment: strconv.FormatInt(xt.ids["local"], 10),
		Action: "GET /api/v1/environments/{env}/containers/{id}/exec", Target: "stopped", Status: http.StatusConflict}}
	if got := keys(auditEntries(t, xt.h, xt.cookie, "").Entries); !slices.Equal(got, want) {
		t.Errorf("audit = %+v, want %+v", got, want)
	}
}
