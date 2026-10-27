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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/probe"
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
	// onRun, if set, is called as a run ends, so a test can change the Docker fake.
	onRun func()
	// upErrs, if set, are the errors of the next ups before they run (nil runs it).
	upErrs []error
	// probes are the HTTP checks asked for; probeErr is their answer.
	probes   []probe.Request
	probeErr error
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
	if f.onRun != nil {
		f.onRun()
	}
	return f.code, f.err
}

func (f *fakeRunner) Up(ctx context.Context, name string, opts stacks.UpOptions, out func(stacks.Line)) (int, error) {
	call := fmt.Sprintf("up %s pull=%v", name, opts.Pull)
	if opts.Wait > 0 {
		call += fmt.Sprintf(" wait=%s", opts.Wait)
	}
	if f.upErrs != nil {
		f.mu.Lock()
		err := f.upErrs[0]
		f.upErrs = f.upErrs[1:]
		f.mu.Unlock()
		if err != nil {
			f.mu.Lock()
			f.calls = append(f.calls, call)
			f.mu.Unlock()
			return 0, err
		}
	}
	return f.run(ctx, call, out)
}

// Probe records the check and answers probeErr.
func (f *fakeRunner) Probe(_ context.Context, _ string, r probe.Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes = append(f.probes, r)
	return f.probeErr
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
	fake    *dockertest.Fake
	dir     string
	runner  *fakeRunner
	checker *fakeChecker
}

// newStackTest has the environments local (the fake runner) and broken (no runner, like
// an agent environment). The local fake has containers of the projects web and other.
// mods change the API's options after the test's own.
func newStackTest(t *testing.T, mods ...func(*Options)) stackTest {
	t.Helper()
	label := func(project, service string) map[string]string {
		return map[string]string{stacks.ProjectLabel: project, stacks.ServiceLabel: service}
	}
	fake := &dockertest.Fake{Resources: dockertest.Resources{Containers: []container.Summary{
		{ID: "a3", Names: []string{"/web-app-2"}, State: container.StateRunning, Labels: label("web", "app")},
		{ID: "a2", Names: []string{"/web-db-1"}, State: container.StateExited, Labels: label("web", "db")},
		{ID: "a1", Names: []string{"/web-app-1"}, State: container.StateExited, Labels: label("web", "app")},
		{ID: "b1", Names: []string{"/other-x-1"}, State: container.StateRunning, Labels: label("other", "x")},
		{ID: "c1", Names: []string{"/loose"}, State: container.StateRunning},
	}}}
	st := stackTest{fake: fake, dir: t.TempDir(), runner: &fakeRunner{}, checker: &fakeChecker{}}
	st.envTest = newEnvTestOpts(t, map[string]*dockertest.Fake{"local": fake, "broken": {}}, func(o *Options) {
		o.StacksDir = st.dir
		o.StackRunner = func(env environments.Environment) stacks.Runner {
			if env.Name != "local" {
				return nil
			}
			return st.runner
		}
		o.StackChecker = st.checker.check
		for _, mod := range mods {
			mod(o)
		}
	})
	return st
}

// fakeChecker answers errs or err and records the files it was asked about.
type fakeChecker struct {
	mu      sync.Mutex
	checked []stacks.Files
	errs    []string
	err     error
}

func (f *fakeChecker) check(_ context.Context, files stacks.Files) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checked = append(f.checked, files)
	return f.errs, f.err
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
			`{"name":"web","containers":0,"running":0,"compose":"services:\n  app: {}\n","env":"TAG=1\n","services":[]}`},
		{"POST", "", `{"name":"web","compose":"services: {}"}`, http.StatusConflict, `{"error":"a stack of that name exists"}`},
		{"POST", "", `{"name":"Web","compose":"services: {}"}`, http.StatusBadRequest, ""},
		{"POST", "", `{"name":"../x","compose":"services: {}"}`, http.StatusBadRequest, ""},
		{"POST", "", `{"name":"db","compose":"  \n"}`, http.StatusBadRequest, `{"error":"compose must not be empty"}`},
		{"POST", "", `{"name":"db","compose":"services: {}\n"}`, http.StatusCreated, ""},
		{"GET", "", "", http.StatusOK, `[{"name":"db","containers":0,"running":0},{"name":"web","containers":3,"running":1}]`},
		{"GET", "/web", "", http.StatusOK, ""},
		{"PUT", "/web", `{"compose":"services:\n  app2: {}\n"}`, http.StatusNoContent, ""},
		{"GET", "/web", "", http.StatusOK, ""},
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
		// errContains is part of the JSON error, if set.
		errContains string
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
		{
			name: "binds can't be mounted", path: "/web/up",
			result: runResult{err: &stacks.BindError{Source: "./data", Path: "/data/stacks/1/web/data",
				Mount: &stacks.Mount{Type: "volume", Name: "nudge-data", Destination: "/data"}}},
			code: http.StatusConflict, call: "up web pull=false", auditCode: http.StatusConflict,
			errContains: `./data can't be deployed: the daemon takes /data/stacks/1/web/data from the host, ` +
				`but nudge has it from the volume nudge-data mounted at /data; ` +
				`mount the stacks directory at the same path on the host and set NUDGE_STACKS_DIR to it`,
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
			if tt.errContains != "" && !strings.Contains(body, tt.errContains) {
				t.Errorf("body = %s, want an error with %q", body, tt.errContains)
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
	if code != http.StatusConflict || !strings.Contains(body, "can't be deployed to this environment") {
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

func TestGetStackServices(t *testing.T) {
	t.Parallel()
	st := newStackTest(t)
	if code, body := st.do(t, "POST", "local", "", `{"name":"web","compose":"services: {}\n","env":"TAG=1\n"}`); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	if code, body := st.do(t, "POST", "local", "", `{"name":"db","compose":"services: {}\n"}`); code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	tests := []struct {
		stack string
		want  map[string][]string // service → container names
		count stackSummaryResponse
	}{
		{"web", map[string][]string{"app": {"web-app-1/exited", "web-app-2/running"}, "db": {"web-db-1/exited"}},
			stackSummaryResponse{Name: "web", Containers: 3, Running: 1}},
		{"db", map[string][]string{}, stackSummaryResponse{Name: "db"}},
	}
	for _, tt := range tests {
		code, body := st.do(t, "GET", "local", "/"+tt.stack, "")
		var got stackResponse
		if err := json.Unmarshal([]byte(body), &got); code != http.StatusOK || err != nil {
			t.Fatalf("GET %s = %d %s (%v)", tt.stack, code, body, err)
		}
		if got.stackSummaryResponse != tt.count || got.Compose != "services: {}\n" {
			t.Errorf("GET %s = %+v, want %+v", tt.stack, got, tt.count)
		}
		if got.Services == nil {
			t.Errorf("GET %s: services is null, want a list", tt.stack)
		}
		services := map[string][]string{}
		var order []string
		for _, svc := range got.Services {
			order = append(order, svc.Name)
			for _, c := range svc.Containers {
				services[svc.Name] = append(services[svc.Name], c.Name+"/"+c.State)
			}
		}
		for name, ctrs := range tt.want {
			if !reflect.DeepEqual(services[name], ctrs) {
				t.Errorf("GET %s: service %s = %q, want %q", tt.stack, name, services[name], ctrs)
			}
		}
		if len(services) != len(tt.want) || !slices.IsSorted(order) {
			t.Errorf("GET %s: services %q, want %d sorted by name", tt.stack, order, len(tt.want))
		}
	}
}

func TestCheckStack(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		errs []string
		err  error
		code int
		want string
	}{
		{"valid", `{"compose":"services: {}\n","env":"A=1\n"}`, []string{}, nil, http.StatusOK, `{"errors":[]}`},
		{"invalid", `{"compose":"services: [\n"}`, []string{"yaml: line 1: did not find expected node content"}, nil,
			http.StatusOK, `{"errors":["yaml: line 1: did not find expected node content"]}`},
		{"empty", `{"compose":" "}`, nil, nil, http.StatusBadRequest, `{"error":"compose must not be empty"}`},
		{"no compose", `{"compose":"services: {}\n"}`, nil, stacks.ErrNoCompose, http.StatusServiceUnavailable,
			`{"error":"docker-compose is not installed"}`},
		{"failure", `{"compose":"services: {}\n"}`, nil, errors.New("disk full at /data"), http.StatusInternalServerError, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := newStackTest(t)
			st.checker.errs, st.checker.err = tt.errs, tt.err
			rec := do(t, st.h, request{method: "POST", path: "/api/v1/stacks/check", body: tt.body, cookie: st.cookie})
			body := strings.TrimSpace(rec.Body.String())
			if rec.Code != tt.code || tt.want != "" && body != tt.want {
				t.Errorf("check = %d %s, want %d %s", rec.Code, body, tt.code, tt.want)
			}
			if strings.Contains(body, "/data") {
				t.Errorf("check leaks the internal error: %s", body)
			}
			if tt.code == http.StatusOK && len(st.checker.checked) != 1 {
				t.Errorf("checker called %d times, want once", len(st.checker.checked))
			}
			// A check changes nothing, so it isn't in the audit log.
			if resp := auditEntries(t, st.h, st.cookie, ""); len(resp.Entries) != 0 {
				t.Errorf("audit log has %+v", resp.Entries)
			}
		})
	}
}

func TestCheckStackUnavailable(t *testing.T) {
	t.Parallel()
	et := newEnvTestOpts(t, map[string]*dockertest.Fake{"local": {}}, nil)
	rec := do(t, et.h, request{method: "POST", path: "/api/v1/stacks/check", body: `{"compose":"services: {}"}`, cookie: et.cookie})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("check without a checker = %d %s, want 503", rec.Code, rec.Body)
	}
}

func TestCheckStackBusy(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	started := make(chan struct{}, maxStackChecks)
	et := newEnvTestOpts(t, map[string]*dockertest.Fake{"local": {}}, func(o *Options) {
		o.StackChecker = func(context.Context, stacks.Files) ([]string, error) {
			started <- struct{}{}
			<-release
			return []string{}, nil
		}
	})
	check := func() int {
		return do(t, et.h, request{method: "POST", path: "/api/v1/stacks/check", body: `{"compose":"services: {}"}`, cookie: et.cookie}).Code
	}
	codes := make(chan int, maxStackChecks)
	for range maxStackChecks {
		go func() { codes <- check() }()
	}
	for range maxStackChecks {
		<-started
	}
	if code := check(); code != http.StatusTooManyRequests {
		t.Errorf("check beyond the limit = %d, want 429", code)
	}
	close(release)
	for range maxStackChecks {
		if code := <-codes; code != http.StatusOK {
			t.Errorf("check within the limit = %d, want 200", code)
		}
	}
	if code := check(); code != http.StatusOK {
		t.Errorf("check after the others finished = %d, want 200", code)
	}
}
