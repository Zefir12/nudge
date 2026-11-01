package stacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/probe"
)

// controlTest is a control service on a Local runner over a temporary directory, served
// by httptest, and a Remote that dials it.
type controlTest struct {
	dir    Dir
	remote *Remote
	srv    *httptest.Server
	mu     sync.Mutex
	// exec runs each compose command; set it before the call.
	exec ExecFunc
	cmds []Command
}

func newControlTest(t *testing.T) *controlTest {
	t.Helper()
	ct := &controlTest{dir: NewDir(t.TempDir())}
	local := NewLocal(LocalOptions{Dir: ct.dir, Compose: "/bin/compose", DockerHost: "unix:///docker.sock", Exec: func(ctx context.Context, cmd Command) (int, error) {
		ct.mu.Lock()
		ct.cmds = append(ct.cmds, cmd)
		exec := ct.exec
		ct.mu.Unlock()
		return exec(ctx, cmd)
	}})
	ct.srv = httptest.NewServer(NewControl(local, nil))
	t.Cleanup(ct.srv.Close)
	addr := ct.srv.Listener.Addr().String()
	ct.remote = NewRemote(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	})
	return ct
}

func (ct *controlTest) setExec(f ExecFunc) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.exec = f
}

func (ct *controlTest) lastArgs() []string {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.cmds[len(ct.cmds)-1].Args
}

// output writes lines and, with them, a "warn" line on stderr, then returns code and err.
func output(lines string, code int, err error) ExecFunc {
	return func(_ context.Context, cmd Command) (int, error) {
		if lines != "" {
			_, _ = cmd.Stdout.Write([]byte(lines))
			_, _ = cmd.Stderr.Write([]byte("warn\n"))
		}
		return code, err
	}
}

func collect() (func(Line), func() []Line) {
	var mu sync.Mutex
	var lines []Line
	return func(l Line) {
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
		}, func() []Line {
			mu.Lock()
			defer mu.Unlock()
			return slices.Clone(lines)
		}
}

func TestRemoteSyncUpDownRemove(t *testing.T) {
	t.Parallel()
	ct := newControlTest(t)
	ctx := context.Background()
	files := Files{Compose: "services:\n  app:\n    image: nginx\n", Env: "A=1\n"}
	if err := ct.remote.Sync(ctx, "web", files); err != nil {
		t.Fatal(err)
	}
	if got, err := ct.dir.Read("web"); err != nil || got != files {
		t.Fatalf("agent's copy = %+v, %v; want %+v", got, err, files)
	}

	ct.setExec(output("one\ntwo\n", 3, nil))
	out, lines := collect()
	code, err := ct.remote.Up(ctx, "web", UpOptions{Pull: true}, out)
	if err != nil || code != 3 {
		t.Fatalf("Up = %d, %v; want 3", code, err)
	}
	if got := lines(); !slices.Contains(got, Line{Stream: "stdout", Text: "one"}) || !slices.Contains(got, Line{Stream: "stdout", Text: "two"}) || !slices.Contains(got, Line{Stream: "stderr", Text: "warn"}) {
		t.Errorf("lines = %+v", got)
	}
	if args := strings.Join(ct.lastArgs(), " "); !strings.HasSuffix(args, "up --detach --remove-orphans --pull always") {
		t.Errorf("up args = %s", args)
	}

	ct.setExec(output("", 0, nil))
	if code, err := ct.remote.Down(ctx, "web", true, func(Line) {}); err != nil || code != 0 {
		t.Fatalf("Down = %d, %v", code, err)
	}
	if args := strings.Join(ct.lastArgs(), " "); !strings.HasSuffix(args, "down --remove-orphans --volumes") {
		t.Errorf("down args = %s", args)
	}

	if err := ct.remote.Remove(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := ct.dir.Read("web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after Remove: %v, want ErrNotFound", err)
	}
	if err := ct.remote.Remove(ctx, "web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Remove = %v, want ErrNotFound", err)
	}
}

func TestRemoteErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tests := []struct {
		name string
		exec ExecFunc
		// sync leaves the stack out when false.
		sync bool
		want func(error) bool
	}{
		{"no such stack", output("", 0, nil), false, func(err error) bool { return errors.Is(err, ErrNotFound) }},
		{"no compose", output("", 0, ErrNoCompose), true, func(err error) bool { return errors.Is(err, ErrNoCompose) }},
		{"compose killed after output", func(_ context.Context, cmd Command) (int, error) {
			_, _ = cmd.Stdout.Write([]byte("started\n"))
			return 0, errors.New("signal: killed")
		}, true, func(err error) bool {
			return err != nil && strings.Contains(err.Error(), "agent: compose did not finish")
		}},
		{"compose failed to start", output("", 0, errors.New("exec format error")), true, func(err error) bool {
			return err != nil && strings.Contains(err.Error(), "agent answered 500: compose could not run")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ct := newControlTest(t)
			if tt.sync {
				if err := ct.remote.Sync(ctx, "web", Files{Compose: "services: {}\n"}); err != nil {
					t.Fatal(err)
				}
			}
			ct.setExec(tt.exec)
			if _, err := ct.remote.Up(ctx, "web", UpOptions{}, func(Line) {}); !tt.want(err) {
				t.Errorf("Up = %v", err)
			}
		})
	}

	t.Run("invalid name", func(t *testing.T) {
		t.Parallel()
		ct := newControlTest(t)
		if err := ct.remote.Sync(ctx, "../x", Files{}); err == nil {
			t.Error("Remote sent an invalid name")
		}
		// The agent checks too.
		req, err := http.NewRequest(http.MethodPut, ct.srv.URL+"/v1/stacks/Bad", strings.NewReader(`{"compose":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("PUT an invalid name = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("agent unreachable", func(t *testing.T) {
		t.Parallel()
		dialErr := &net.OpError{Op: "dial", Net: "tunnel", Err: errors.New("agent not connected")}
		r := NewRemote(func(context.Context) (net.Conn, error) { return nil, dialErr })
		err := r.Sync(ctx, "web", Files{})
		var op *net.OpError
		if !errors.As(err, &op) {
			t.Errorf("Sync = %v, want the dial's *net.OpError", err)
		}
	})
}

func TestRemoteCredentials(t *testing.T) {
	t.Parallel()
	gh := Credential{Address: "ghcr.io", Username: "bot", Password: "gh-pass"}
	files := Files{Compose: "services:\n  app:\n    image: ghcr.io/org/app\n"}
	tests := []struct {
		name  string
		creds []Credential
		// set is whether the Remote has a credentials lookup.
		set        bool
		down       bool
		wantBody   string
		wantConfig string
	}{
		{name: "no lookup", wantBody: `{"pull":true}`},
		{name: "none found", set: true, creds: []Credential{}, wantBody: `{"pull":true}`},
		{name: "found", set: true, creds: []Credential{gh},
			wantBody:   `{"pull":true,"credentials":[{"address":"ghcr.io","username":"bot","password":"gh-pass"}]}`,
			wantConfig: `{"auths":{"ghcr.io":{"auth":"Ym90OmdoLXBhc3M="}}}`},
		{name: "down", set: true, creds: []Credential{gh}, down: true, wantBody: `{"volumes":false}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			agentDir := NewDir(t.TempDir())
			var run configRun
			local := NewLocal(LocalOptions{Dir: agentDir, Exec: seeConfig(t, &run, "login gh-pass\n")})
			control := NewControl(local, nil)
			bodies := make(chan string, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					b, _ := io.ReadAll(r.Body)
					bodies <- strings.TrimSpace(string(b))
					r.Body = io.NopCloser(bytes.NewReader(b))
				}
				control.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)
			addr := srv.Listener.Addr().String()
			remote := NewRemote(func(ctx context.Context) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", addr)
			})
			serverDir := NewDir(t.TempDir())
			if err := serverDir.Create("web", files); err != nil {
				t.Fatal(err)
			}
			if tt.set {
				remote = remote.WithCredentials(serverDir, func(_ context.Context, f Files) ([]Credential, error) {
					if f != files {
						t.Errorf("Credentials got %+v, want the server's copy of the stack", f)
					}
					return tt.creds, nil
				})
			}
			ctx := context.Background()
			if err := remote.Sync(ctx, "web", files); err != nil {
				t.Fatal(err)
			}
			out, lines := collect()
			var err error
			if tt.down {
				_, err = remote.Down(ctx, "web", false, out)
			} else {
				_, err = remote.Up(ctx, "web", UpOptions{Pull: true}, out)
			}
			if err != nil {
				t.Fatal(err)
			}
			if body := <-bodies; body != tt.wantBody {
				t.Errorf("request body = %s, want %s", body, tt.wantBody)
			}
			if seen := run.get(); seen.config != tt.wantConfig {
				t.Errorf("agent's config.json = %s, want %s", seen.config, tt.wantConfig)
			}
			if tt.wantConfig != "" {
				for _, l := range lines() {
					if strings.Contains(l.Text, "gh-pass") {
						t.Errorf("output line %q has the password", l.Text)
					}
				}
			}
		})
	}

	t.Run("agent too old", func(t *testing.T) {
		t.Parallel()
		// An agent from before credentials: its up body has only "pull".
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			var req struct {
				Pull bool `json:"pull"`
			}
			if err := dec.Decode(&req); err != nil {
				controlError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write([]byte(`{"exit_code":0}` + "\n"))
		}))
		t.Cleanup(srv.Close)
		addr := srv.Listener.Addr().String()
		dir := NewDir(t.TempDir())
		if err := dir.Create("web", files); err != nil {
			t.Fatal(err)
		}
		for _, tt := range []struct {
			name    string
			creds   []Credential
			wantOld bool
		}{
			{"with credentials", []Credential{gh}, true},
			{"without", nil, false},
		} {
			remote := NewRemote(func(ctx context.Context) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", addr)
			}).WithCredentials(dir, func(context.Context, Files) ([]Credential, error) { return tt.creds, nil })
			_, err := remote.Up(context.Background(), "web", UpOptions{}, func(Line) {})
			if got := errors.Is(err, ErrOldAgent); got != tt.wantOld || (!tt.wantOld && err != nil) {
				t.Errorf("%s: Up = %v, want ErrOldAgent %v", tt.name, err, tt.wantOld)
			}
		}
	})

	t.Run("lookup fails", func(t *testing.T) {
		t.Parallel()
		remote := NewRemote(func(context.Context) (net.Conn, error) {
			t.Error("dialled the agent after a failed lookup")
			return nil, errors.New("no")
		})
		dir := NewDir(t.TempDir())
		if err := dir.Create("web", files); err != nil {
			t.Fatal(err)
		}
		remote = remote.WithCredentials(dir, func(context.Context, Files) ([]Credential, error) {
			return nil, errors.New("sealed with another key")
		})
		if _, err := remote.Up(context.Background(), "web", UpOptions{}, func(Line) {}); err == nil {
			t.Error("Up with a failing lookup succeeded")
		}
	})
}

func TestControlOneChangePerStack(t *testing.T) {
	t.Parallel()
	ct := newControlTest(t)
	ctx := context.Background()
	if err := ct.remote.Sync(ctx, "web", Files{Compose: "services: {}\n"}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	ct.setExec(func(_ context.Context, cmd Command) (int, error) {
		_, _ = cmd.Stdout.Write([]byte("working\n"))
		close(started)
		<-release
		return 0, nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := ct.remote.Up(ctx, "web", UpOptions{}, func(Line) {})
		done <- err
	}()
	<-started
	if _, err := ct.remote.Down(ctx, "web", false, func(Line) {}); !errors.Is(err, ErrBusy) {
		t.Errorf("Down during Up = %v, want ErrBusy", err)
	}
	if err := ct.remote.Sync(ctx, "web", Files{Compose: "x"}); !errors.Is(err, ErrBusy) {
		t.Errorf("Sync during Up = %v, want ErrBusy", err)
	}
	// Another stack isn't held up.
	if err := ct.remote.Sync(ctx, "other", Files{Compose: "services: {}\n"}); err != nil {
		t.Errorf("Sync of another stack: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ct.setExec(output("", 0, nil))
	if _, err := ct.remote.Down(ctx, "web", false, func(Line) {}); err != nil {
		t.Errorf("Down after Up: %v", err)
	}
}

func TestRemoteCancelKillsCompose(t *testing.T) {
	t.Parallel()
	ct := newControlTest(t)
	if err := ct.remote.Sync(context.Background(), "web", Files{Compose: "services: {}\n"}); err != nil {
		t.Fatal(err)
	}
	killed := make(chan struct{})
	ct.setExec(func(ctx context.Context, cmd Command) (int, error) {
		_, _ = cmd.Stdout.Write([]byte("pulling\n"))
		<-ctx.Done()
		close(killed)
		return 0, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Line, 1)
	done := make(chan error, 1)
	go func() {
		_, err := ct.remote.Up(ctx, "web", UpOptions{}, func(l Line) {
			select {
			case got <- l:
			default:
			}
		})
		done <- err
	}()
	<-got
	cancel()
	select {
	case <-killed:
	case <-time.After(5 * time.Second):
		t.Fatal("compose kept running after the server cancelled")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Up = %v, want context.Canceled", err)
	}
}

// finalLineWriter records, when the final exit_code line of a change is written, the
// status of a DELETE of the same stack: the client may send it as soon as it reads the line.
type finalLineWriter struct {
	*httptest.ResponseRecorder
	handler http.Handler
	status  int
}

func (w *finalLineWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"exit_code"`)) {
		rec := httptest.NewRecorder()
		w.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/stacks/web", nil))
		w.status = rec.Code
	}
	return w.ResponseRecorder.Write(p)
}

func TestControlReleasesStackBeforeFinalLine(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"up", "down"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			dir := NewDir(t.TempDir())
			if err := dir.Write("web", Files{Compose: "services: {}\n"}); err != nil {
				t.Fatal(err)
			}
			local := NewLocal(LocalOptions{Dir: dir, Compose: "/bin/compose", DockerHost: "unix:///docker.sock", Exec: output("", 0, nil)})
			h := NewControl(local, nil)
			w := &finalLineWriter{ResponseRecorder: httptest.NewRecorder(), handler: h}
			h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/stacks/web/"+op, strings.NewReader("{}")))
			if w.status != http.StatusNoContent {
				t.Errorf("DELETE while the %s answer is sent = %d, want %d", op, w.status, http.StatusNoContent)
			}
		})
	}
}

// probeServer serves handler and returns a Remote that calls it.
func probeServer(t *testing.T, handler http.Handler) *Remote {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	return NewRemote(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	})
}

func TestRemoteProbe(t *testing.T) {
	t.Parallel()
	req := probe.Request{Container: "0123456789ab", Port: 8080, Path: "/health", Status: 200, Timeout: 5 * time.Second}
	tests := []struct {
		name    string
		handler func(got chan<- probe.Request) http.Handler
		want    func(error) bool
	}{
		{"passes", func(got chan<- probe.Request) http.Handler {
			return NewControl(NewLocal(LocalOptions{Dir: NewDir(t.TempDir()), Probe: func(_ context.Context, _ string, r probe.Request) error {
				got <- r
				return nil
			}}), nil)
		}, func(err error) bool { return err == nil }},
		{"fails", func(got chan<- probe.Request) http.Handler {
			return NewControl(NewLocal(LocalOptions{Dir: NewDir(t.TempDir()), Probe: func(_ context.Context, _ string, r probe.Request) error {
				got <- r
				return &probe.FailedError{Msg: "answered another status than 200"}
			}}), nil)
		}, func(err error) bool {
			var fe *probe.FailedError
			return errors.As(err, &fe) && fe.Msg == "answered another status than 200"
		}},
		{"agent outside a container", func(chan<- probe.Request) http.Handler {
			return NewControl(NewLocal(LocalOptions{Dir: NewDir(t.TempDir())}), nil)
		}, func(err error) bool { return errors.Is(err, probe.ErrNoSelf) }},
		{"check broken", func(chan<- probe.Request) http.Handler {
			return NewControl(NewLocal(LocalOptions{Dir: NewDir(t.TempDir()), Probe: func(context.Context, string, probe.Request) error {
				return errors.New("daemon said: secret stuff")
			}}), nil)
		}, func(err error) bool { return err != nil && !strings.Contains(err.Error(), "secret") }},
		{"old agent", func(chan<- probe.Request) http.Handler {
			// An agent from before checks: the catch-all 404 of its control service.
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { controlError(w, http.StatusNotFound, "not found") })
		}, func(err error) bool { return errors.Is(err, ErrNoProbe) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := make(chan probe.Request, 1)
			err := probeServer(t, tt.handler(got)).Probe(context.Background(), "web", req)
			if !tt.want(err) {
				t.Fatalf("Probe = %v", err)
			}
			select {
			case r := <-got:
				if r != req {
					t.Errorf("agent got %+v, want %+v", r, req)
				}
			default:
			}
		})
	}
}

func TestControlRefusesBadProbe(t *testing.T) {
	t.Parallel()
	h := NewControl(NewLocal(LocalOptions{Dir: NewDir(t.TempDir()), Probe: func(context.Context, string, probe.Request) error { return nil }}), nil)
	for _, body := range []string{`{"container":"x","port":80,"path":"/","status":200,"timeout":1000000000}`, `{"unknown":1}`, `not json`} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/stacks/web/probe", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", body, rec.Code)
		}
	}
}

func TestRemoteUpWaitOldAgent(t *testing.T) {
	t.Parallel()
	// An agent from before --wait refuses the unknown field.
	remote := probeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "wait_seconds") {
			controlError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"exit_code":0}` + "\n"))
	}))
	if _, err := remote.Up(context.Background(), "web", UpOptions{Pull: true, Wait: time.Minute}, func(Line) {}); !errors.Is(err, ErrNoWait) {
		t.Fatalf("Up with wait = %v, want ErrNoWait", err)
	}
	if code, err := remote.Up(context.Background(), "web", UpOptions{Pull: true}, func(Line) {}); err != nil || code != 0 {
		t.Fatalf("Up without wait = %d, %v", code, err)
	}
}
