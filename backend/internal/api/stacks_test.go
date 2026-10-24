package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

// fakeRunner records what it is asked to do and answers with lines, then code or err.
type fakeRunner struct {
	mu      sync.Mutex
	calls   []string
	synced  map[string]stacks.Files
	removed []string

	lines []stacks.Line
	code  int
	err   error
	// started, if set, gets a value when a run starts; the run then waits for release.
	started chan struct{}
	release chan struct{}
	// cancelled, if set, is closed when a waiting run is cancelled.
	cancelled chan struct{}
}

func (f *fakeRunner) Sync(_ context.Context, name string, files stacks.Files) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.synced == nil {
		f.synced = map[string]stacks.Files{}
	}
	f.synced[name] = files
	return nil
}

func (f *fakeRunner) run(ctx context.Context, call string, out func(stacks.Line)) (int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
		select {
		case <-f.release:
		case <-ctx.Done():
			if f.cancelled != nil {
				close(f.cancelled)
			}
			return 0, ctx.Err()
		}
	}
	for _, l := range f.lines {
		out(l)
	}
	return f.code, f.err
}

func (f *fakeRunner) Up(ctx context.Context, name string, pull bool, out func(stacks.Line)) (int, error) {
	return f.run(ctx, fmt.Sprintf("up %s pull=%v", name, pull), out)
}

func (f *fakeRunner) Down(ctx context.Context, name string, volumes bool, out func(stacks.Line)) (int, error) {
	return f.run(ctx, fmt.Sprintf("down %s volumes=%v", name, volumes), out)
}

func (f *fakeRunner) Remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	return nil
}

// runResult is what a fakeRunner run answers.
type runResult struct {
	lines []stacks.Line
	code  int
	err   error
}

type stackTest struct {
	envTest
	dir    string
	runner *fakeRunner
}

// newStackTest has the environments local (the fake runner) and broken (no runner, like
// an agent environment). The local fake has containers of the projects web and other.
func newStackTest(t *testing.T) stackTest {
	t.Helper()
	label := func(project string) map[string]string { return map[string]string{stacks.ProjectLabel: project} }
	fake := &dockertest.Fake{Resources: dockertest.Resources{Containers: []container.Summary{
		{ID: "a1", Names: []string{"/web-app-1"}, State: container.StateRunning, Labels: label("web")},
		{ID: "a2", Names: []string{"/web-db-1"}, State: container.StateExited, Labels: label("web")},
		{ID: "b1", Names: []string{"/other-x-1"}, State: container.StateRunning, Labels: label("other")},
		{ID: "c1", Names: []string{"/loose"}, State: container.StateRunning},
	}}}
	st := stackTest{dir: t.TempDir(), runner: &fakeRunner{}}
	st.envTest = newEnvTestOpts(t, map[string]*dockertest.Fake{"local": fake, "broken": {}}, func(o *Options) {
		o.StacksDir = st.dir
		o.StackRunner = func(env environments.Environment) stacks.Runner {
			if env.Name != "local" {
				return nil
			}
			return st.runner
		}
	})
	return st
}

func (st stackTest) do(t *testing.T, method, env, path, body string) (int, string) {
	t.Helper()
	rec := do(t, st.h, request{method: method, path: fmt.Sprintf("/api/v1/environments/%d/stacks%s", st.ids[env], path), body: body, cookie: st.cookie})
	return rec.Code, rec.Body.String()
}

// stackEvents splits an NDJSON stream.
func stackEvents(t *testing.T, body string) []stackEvent {
	t.Helper()
	var evs []stackEvent
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		var ev stackEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func TestStackFiles(t *testing.T) {
	t.Parallel()
	st := newStackTest(t)
	steps := []struct {
		method, path, body string
		code               int
		want               string
	}{
		{"GET", "", "", http.StatusOK, `[]`},
		{"POST", "", `{"name":"web","compose":"services:\n  app: {}\n","env":"TAG=1\n"}`, http.StatusCreated,
			`{"name":"web","containers":0,"running":0,"compose":"services:\n  app: {}\n","env":"TAG=1\n"}`},
		{"POST", "", `{"name":"web","compose":"services: {}"}`, http.StatusConflict, `{"error":"a stack of that name exists"}`},
		{"POST", "", `{"name":"Web","compose":"services: {}"}`, http.StatusBadRequest, ""},
		{"POST", "", `{"name":"../x","compose":"services: {}"}`, http.StatusBadRequest, ""},
		{"POST", "", `{"name":"db","compose":"  \n"}`, http.StatusBadRequest, `{"error":"compose must not be empty"}`},
		{"POST", "", `{"name":"db","compose":"services: {}\n"}`, http.StatusCreated, ""},
		{"GET", "", "", http.StatusOK, `[{"name":"db","containers":0,"running":0},{"name":"web","containers":2,"running":1}]`},
		{"GET", "/web", "", http.StatusOK,
			`{"name":"web","containers":2,"running":1,"compose":"services:\n  app: {}\n","env":"TAG=1\n"}`},
		{"PUT", "/web", `{"compose":"services:\n  app2: {}\n"}`, http.StatusNoContent, ""},
		{"GET", "/web", "", http.StatusOK, `{"name":"web","containers":2,"running":1,"compose":"services:\n  app2: {}\n","env":""}`},
		{"PUT", "/web", `{"compose":""}`, http.StatusBadRequest, ""},
		{"PUT", "/nope", `{"compose":"services: {}"}`, http.StatusNotFound, `{"error":"stack not found"}`},
		{"GET", "/nope", "", http.StatusNotFound, `{"error":"stack not found"}`},
		{"GET", "/Web", "", http.StatusNotFound, `{"error":"stack not found"}`},
	}
	for _, s := range steps {
		code, body := st.do(t, s.method, "local", s.path, s.body)
		if code != s.code || s.want != "" && strings.TrimSpace(body) != s.want {
			t.Errorf("%s %s = %d %s, want %d %s", s.method, s.path, code, body, s.code, s.want)
		}
	}
	compose, err := os.ReadFile(filepath.Join(st.dir, fmt.Sprint(st.ids["local"]), "web", "compose.yaml"))
	if err != nil || string(compose) != "services:\n  app2: {}\n" {
		t.Errorf("compose.yaml on disk = %q, %v", compose, err)
	}
	// Stacks are per environment.
	if code, body := st.do(t, "GET", "broken", "", ""); code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Errorf("stacks of another environment = %d %s, want none", code, body)
	}
}

func TestStackUpDown(t *testing.T) {
	t.Parallel()
	exit := func(n int) *int { return &n }
	tests := []struct {
		name       string
		path, body string
		result     runResult
		code       int
		call       string
		want       []stackEvent
		auditCode  int
	}{
		{
			name: "up", path: "/web/up",
			result: runResult{lines: []stacks.Line{{Stream: "stderr", Text: " Container web-app-1  Started"}}},
			code:   http.StatusOK, call: "up web pull=false", auditCode: http.StatusOK,
			want: []stackEvent{{Stream: "stderr", Line: " Container web-app-1  Started"}, {ExitCode: exit(0)}},
		},
		{
			name: "update pulls", path: "/web/up", body: `{"pull":true}`,
			code: http.StatusOK, call: "up web pull=true", auditCode: http.StatusOK,
			want: []stackEvent{{ExitCode: exit(0)}},
		},
		{
			name: "down with volumes", path: "/web/down", body: `{"volumes":true}`,
			code: http.StatusOK, call: "down web volumes=true", auditCode: http.StatusOK,
			want: []stackEvent{{ExitCode: exit(0)}},
		},
		{
			name: "compose fails", path: "/web/up",
			result: runResult{lines: []stacks.Line{{Stream: "stderr", Text: "no such image"}}, code: 1},
			code:   http.StatusOK, call: "up web pull=false", auditCode: http.StatusBadGateway,
			want: []stackEvent{{Stream: "stderr", Line: "no such image"}, {ExitCode: exit(1)}},
		},
		{
			name: "killed after output", path: "/web/up",
			result: runResult{lines: []stacks.Line{{Stream: "stdout", Text: "pulling"}}, err: errors.New("signal: killed")},
			code:   http.StatusOK, call: "up web pull=false", auditCode: http.StatusBadGateway,
			want: []stackEvent{{Stream: "stdout", Line: "pulling"}, {Error: "compose did not finish"}},
		},
		{
			name: "no compose", path: "/web/up", result: runResult{err: fmt.Errorf("compose up web: %w", stacks.ErrNoCompose)},
			code: http.StatusServiceUnavailable, call: "up web pull=false", auditCode: http.StatusServiceUnavailable,
		},
		{name: "missing stack", path: "/nope/up", code: http.StatusNotFound, auditCode: http.StatusNotFound},
		{name: "bad body", path: "/web/up", body: `{"pull":"yes"}`, code: http.StatusBadRequest, auditCode: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := newStackTest(t)
			st.runner.lines, st.runner.code, st.runner.err = tt.result.lines, tt.result.code, tt.result.err
			if code, body := st.do(t, "POST", "local", "", `{"name":"web","compose":"services: {}\n","env":"A=1\n"}`); code != http.StatusCreated {
				t.Fatalf("create = %d %s", code, body)
			}
			code, body := st.do(t, "POST", "local", tt.path, tt.body)
			if code != tt.code {
				t.Fatalf("POST %s = %d %s, want %d", tt.path, code, body, tt.code)
			}
			if tt.want != nil {
				if got := stackEvents(t, body); !reflect.DeepEqual(got, tt.want) {
					t.Errorf("events = %+v, want %+v", got, tt.want)
				}
			}
			if tt.call != "" {
				if !reflect.DeepEqual(st.runner.calls, []string{tt.call}) {
					t.Errorf("runner calls = %q, want %q", st.runner.calls, tt.call)
				}
				if got := st.runner.synced["web"]; got != (stacks.Files{Compose: "services: {}\n", Env: "A=1\n"}) {
					t.Errorf("synced files = %+v", got)
				}
			}
			if got := lastAuditStatus(t, st.envTest); got != tt.auditCode {
				t.Errorf("audit status = %d, want %d", got, tt.auditCode)
			}
		})
	}
}

// lastAuditStatus is the status of the newest audit entry.
func lastAuditStatus(t *testing.T, et envTest) int {
	t.Helper()
	rec := do(t, et.h, request{method: "GET", path: "/api/v1/audit?limit=1", cookie: et.cookie})
	var resp auditListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Entries) != 1 {
		t.Fatalf("audit = %d %s, %v", rec.Code, rec.Body, err)
	}
	return resp.Entries[0].Status
}

func TestStackWithoutRunner(t *testing.T) {
	t.Parallel()
	st := newStackTest(t)
	if code, body := st.do(t, "POST", "broken", "", `{"name":"web","compose":"services: {}\n"}`); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	code, body := st.do(t, "POST", "broken", "/web/up", "")
	if code != http.StatusConflict || !strings.Contains(body, "agent environments") {
		t.Errorf("up without a runner = %d %s, want 409", code, body)
	}
	if code, body := st.do(t, "DELETE", "broken", "/web", ""); code != http.StatusNoContent {
		t.Errorf("remove without a runner = %d %s, want 204", code, body)
	}
}

func TestStackBusy(t *testing.T) {
	t.Parallel()
	st := newStackTest(t)
	st.runner.started, st.runner.release = make(chan struct{}), make(chan struct{})
	if code, body := st.do(t, "POST", "local", "", `{"name":"web","compose":"services: {}\n"}`); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	done := make(chan int)
	go func() {
		rec := do(t, st.h, request{method: "POST", path: fmt.Sprintf("/api/v1/environments/%d/stacks/web/up", st.ids["local"]), cookie: st.cookie})
		done <- rec.Code
	}()
	<-st.runner.started
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/web/up", ""},
		{"POST", "/web/down", ""},
		{"PUT", "/web", `{"compose":"services: {}\n"}`},
		{"DELETE", "/web", ""},
	} {
		if code, body := st.do(t, r.method, "local", r.path, r.body); code != http.StatusConflict {
			t.Errorf("%s %s during a deploy = %d %s, want 409", r.method, r.path, code, body)
		}
	}
	close(st.runner.release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("first up = %d", code)
	}
	st.runner.started = nil
	if code, body := st.do(t, "POST", "local", "/web/up", ""); code != http.StatusOK {
		t.Errorf("up after the first ended = %d %s, want 200", code, body)
	}
}

func TestRemoveStack(t *testing.T) {
	t.Parallel()
	st := newStackTest(t)
	for _, name := range []string{"web", "db"} {
		if code, body := st.do(t, "POST", "local", "", `{"name":"`+name+`","compose":"services: {}\n"}`); code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", name, code, body)
		}
	}
	if code, body := st.do(t, "DELETE", "local", "/web", ""); code != http.StatusConflict || !strings.Contains(body, "take it down first") {
		t.Errorf("remove with containers = %d %s, want 409", code, body)
	}
	if code, body := st.do(t, "DELETE", "local", "/db", ""); code != http.StatusNoContent {
		t.Fatalf("remove = %d %s, want 204", code, body)
	}
	if !reflect.DeepEqual(st.runner.removed, []string{"db"}) {
		t.Errorf("runner removed %q, want [db]", st.runner.removed)
	}
	if code, _ := st.do(t, "GET", "local", "/db", ""); code != http.StatusNotFound {
		t.Errorf("get after remove = %d, want 404", code)
	}
	if code, _ := st.do(t, "DELETE", "local", "/db", ""); code != http.StatusNotFound {
		t.Errorf("second remove = %d, want 404", code)
	}
}

func TestStackClientGone(t *testing.T) {
	t.Parallel()
	st := newStackTest(t)
	st.runner.started, st.runner.release, st.runner.cancelled = make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	if code, body := st.do(t, "POST", "local", "", `{"name":"web","compose":"services: {}\n"}`); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	srv := httptest.NewServer(st.h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/v1/environments/%d/stacks/web/up", srv.URL, st.ids["local"]), strings.NewReader(`{"pull":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(st.cookie)
	errc := make(chan error, 1)
	go func() {
		resp, err := srv.Client().Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		errc <- err
	}()
	<-st.runner.started
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("request = %v, want cancelled", err)
	}
	select {
	case <-st.runner.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("compose kept running after the client went away")
	}
	// The entry is recorded once the handler returns.
	deadline := time.Now().Add(5 * time.Second)
	for lastAuditStatus(t, st.envTest) != 499 {
		if time.Now().After(deadline) {
			t.Fatalf("audit status = %d, want 499", lastAuditStatus(t, st.envTest))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
