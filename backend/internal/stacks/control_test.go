package stacks

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
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
	code, err := ct.remote.Up(ctx, "web", true, out)
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
			if _, err := ct.remote.Up(ctx, "web", false, func(Line) {}); !tt.want(err) {
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
		_, err := ct.remote.Up(ctx, "web", false, func(Line) {})
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
		_, err := ct.remote.Up(ctx, "web", false, func(l Line) {
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
