package probe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const target = "0123456789abcdef"

// fakeDocker knows nudge's container (self) and runs every helper to exitCode after
// polls inspections.
type fakeDocker struct {
	mu       sync.Mutex
	exitCode int
	polls    int
	never    bool
	created  []client.ContainerCreateOptions
	removed  []string
	inspects int
	startErr error
}

func (f *fakeDocker) ContainerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == target || id == "other" {
		return client.ContainerInspectResult{Container: container.InspectResponse{Config: &container.Config{Labels: map[string]string{ProjectLabel: "web"}}}}, nil
	}
	if id == "self" {
		return client.ContainerInspectResult{Container: container.InspectResponse{Image: "sha256:nudge", Config: &container.Config{Hostname: "nudge"}}}, nil
	}
	if id != "helper" {
		return client.ContainerInspectResult{}, errors.New("no such container")
	}
	f.inspects++
	st := &container.State{Status: container.StateRunning, Running: true}
	if !f.never && f.inspects > f.polls {
		st = &container.State{Status: container.StateExited, ExitCode: f.exitCode}
	}
	return client.ContainerInspectResult{Container: container.InspectResponse{State: st}}, nil
}

func (f *fakeDocker) ContainerCreate(_ context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, opts)
	return client.ContainerCreateResult{ID: "helper"}, nil
}

func (f *fakeDocker) ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, f.startErr
}

func (f *fakeDocker) ContainerRemove(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return client.ContainerRemoveResult{}, nil
}

// polls fires the pauses between polls at once and never the deadline.
func polls(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	if d == pollEvery {
		ch <- time.Time{}
	}
	return ch
}

func request() Request {
	return Request{Container: target, Port: 8080, Path: "/health", Status: 200, Timeout: 5 * time.Second}
}

func TestRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		docker  *fakeDocker
		self    string
		limitOK bool
		want    func(error) bool
	}{
		{"healthy", &fakeDocker{exitCode: ExitOK, polls: 2}, "self", false, func(err error) bool { return err == nil }},
		{"wrong status", &fakeDocker{exitCode: ExitStatus}, "self", false, failedWith("another status than 200")},
		{"no answer", &fakeDocker{exitCode: ExitNoAnswer}, "self", false, failedWith("didn't answer on port 8080")},
		{"helper never ends", &fakeDocker{never: true}, "self", true, failedWith("didn't finish in time")},
		{"helper broken", &fakeDocker{exitCode: 1}, "self", false, func(err error) bool {
			var fe *FailedError
			return err != nil && !errors.As(err, &fe)
		}},
		{"outside a container", &fakeDocker{}, "", false, func(err error) bool { return errors.Is(err, ErrNoSelf) }},
		{"another daemon's container", &fakeDocker{}, "other", false, func(err error) bool { return errors.Is(err, ErrNoSelf) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := tt.docker
			after := polls
			if tt.limitOK {
				// The deadline fires; polls never do.
				after = func(d time.Duration) <-chan time.Time {
					ch := make(chan time.Time, 1)
					if d != pollEvery {
						ch <- time.Time{}
					}
					return ch
				}
			}
			p := New(Options{Docker: func() (Docker, error) { return f, nil }, Self: tt.self, Hostname: "nudge", After: after})
			err := p.Run(context.Background(), "web", request())
			if !tt.want(err) {
				t.Fatalf("Run = %v", err)
			}
			if tt.self != "self" {
				return
			}
			if len(f.created) != 1 {
				t.Fatalf("created %d helpers", len(f.created))
			}
			c := f.created[0]
			if c.Config.Image != "sha256:nudge" || string(c.HostConfig.NetworkMode) != "container:"+target {
				t.Errorf("helper = image %q network %q", c.Config.Image, c.HostConfig.NetworkMode)
			}
			if got := strings.Join(c.Config.Cmd, " "); got != "probe -status 200 -timeout 5s http://127.0.0.1:8080/health" {
				t.Errorf("cmd = %q", got)
			}
			if len(f.removed) != 1 || f.removed[0] != "helper" {
				t.Errorf("removed = %v", f.removed)
			}
		})
	}
}

func TestRunRemovesHelperWhenStartFails(t *testing.T) {
	t.Parallel()
	f := &fakeDocker{startErr: errors.New("boom")}
	p := New(Options{Docker: func() (Docker, error) { return f, nil }, Self: "self", Hostname: "nudge", After: polls})
	if err := p.Run(context.Background(), "web", request()); err == nil {
		t.Fatal("Run succeeded")
	}
	if len(f.removed) != 1 {
		t.Errorf("removed = %v", f.removed)
	}
}

func TestRunRefusesAnotherProjectsContainer(t *testing.T) {
	t.Parallel()
	f := &fakeDocker{}
	p := New(Options{Docker: func() (Docker, error) { return f, nil }, Self: "self", Hostname: "nudge", After: polls})
	if err := p.Run(context.Background(), "shop", request()); !failedWith("isn't one of the stack's")(err) {
		t.Fatalf("Run = %v", err)
	}
	if len(f.created) != 0 {
		t.Errorf("created %d helpers", len(f.created))
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*Request)
		ok   bool
	}{
		{"valid", func(*Request) {}, true},
		{"query", func(r *Request) { r.Path = "/health?full=1" }, true},
		{"relative path", func(r *Request) { r.Path = "health" }, false},
		{"space in path", func(r *Request) { r.Path = "/a b" }, false},
		{"port", func(r *Request) { r.Port = 0 }, false},
		{"status", func(r *Request) { r.Status = 99 }, false},
		{"timeout", func(r *Request) { r.Timeout = 2 * time.Minute }, false},
		{"container", func(r *Request) { r.Container = "web; rm" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := request()
			tt.edit(&r)
			if err := r.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/moved":
			http.Redirect(w, r, "/ok", http.StatusFound)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	tests := []struct {
		name   string
		url    string
		status int
		want   int
	}{
		{"ok", srv.URL + "/ok", 200, ExitOK},
		{"redirect not followed", srv.URL + "/moved", 302, ExitOK},
		{"wrong status", srv.URL + "/down", 200, ExitStatus},
		{"no answer", closedURL, 200, ExitNoAnswer},
		{"bad url", "http://[::1", 200, ExitBadInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if code, msg := Check(context.Background(), tt.url, tt.status, 5*time.Second); code != tt.want {
				t.Errorf("Check = %d (%s), want %d", code, msg, tt.want)
			}
		})
	}
}

func failedWith(s string) func(error) bool {
	return func(err error) bool {
		var fe *FailedError
		return errors.As(err, &fe) && strings.Contains(fe.Msg, s)
	}
}
