package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"git.zefirlabs.net/zefir/nudge/backend/internal/deployments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/probe"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

// healthTest is a deployment test whose stack comes up as the running container a2 of
// service app; onWindow runs when the stability window starts waiting, then the window
// ends at once.
type healthTest struct {
	stackTest
	token    string
	onWindow func()
}

func newHealthTest(t *testing.T, health string) *healthTest {
	t.Helper()
	ht := &healthTest{}
	ht.stackTest = newDeploymentTest(t, func(o *Options) {
		o.After = func(d time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			if d == deployKeepAlive {
				return ch
			}
			if ht.onWindow != nil {
				ht.onWindow()
			}
			ch <- time.Time{}
			return ch
		}
	})
	label := map[string]string{stacks.ProjectLabel: "web", stacks.ServiceLabel: "app"}
	ht.runner.onRun = func() {
		ht.fake.Containers = []container.Summary{{ID: "a2", Image: "app:v2", ImageID: "sha256:a2", State: container.StateRunning, Labels: label}}
		ht.fake.ContainerDetails = map[string]container.InspectResponse{
			"a2": {ID: "a2", State: &container.State{Status: container.StateRunning, Running: true}},
		}
	}
	rec := do(t, ht.h, request{method: "PATCH", path: "/api/v1/deployments/shop", cookie: ht.cookie, body: `{"health":` + health + `}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("set health = %d %s", rec.Code, rec.Body)
	}
	ht.token = deployToken(t, ht.stackTest, "ci", "deploy")
	return ht
}

// deploy runs a deploy of API_IMAGE=app:v2 with ?wait=1.
func (ht *healthTest) deploy(t *testing.T) (int, deploymentRunJSON) {
	t.Helper()
	rec := do(t, ht.h, request{method: "POST", path: "/api/v1/deployments/shop/deploys?wait=1", body: `{"variables":{"API_IMAGE":"app:v2"}}`, headers: bearer(ht.token)})
	return rec.Code, deployRunOf(t, rec.Body.String())
}

// env is the server's copy of the stack's .env.
func (ht *healthTest) env(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ht.dir, strconv.FormatInt(ht.ids["local"], 10), "web", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	envV1 = "# pinned by CI\nAPI_IMAGE=app:v1\nKEEP=1\n"
	envV2 = "# pinned by CI\nAPI_IMAGE=app:v2\nKEEP=1\n"
)

func TestDeployHealth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		health string
		setup  func(ht *healthTest)
		code   int
		status string
		err    string
		calls  []string
		env    string
		check  func(t *testing.T, ht *healthTest, run deploymentRunJSON)
	}{
		{
			name: "compose waits", health: `{"wait_seconds":60}`,
			code: 200, status: "succeeded", calls: []string{"up web pull=true wait=1m0s"}, env: envV2,
		},
		{
			name: "agent too old to wait", health: `{"wait_seconds":60}`,
			setup: func(ht *healthTest) { ht.runner.upErrs = []error{stacks.ErrNoWait, nil} },
			code:  200, status: "succeeded", calls: []string{"up web pull=true wait=1m0s", "up web pull=true"}, env: envV2,
			check: func(t *testing.T, _ *healthTest, run deploymentRunJSON) {
				if len(run.Warnings) != 1 || !strings.Contains(run.Warnings[0], "too old to wait") {
					t.Errorf("warnings = %q", run.Warnings)
				}
			},
		},
		{
			name: "compose fails waiting on a service", health: `{"wait_seconds":60}`,
			setup: func(ht *healthTest) {
				ht.runner.code = 1
				ht.runner.lines = []stacks.Line{{Stream: "stderr", Text: " Container web-app-1  Recreate"}}
				onRun := ht.runner.onRun
				ht.runner.onRun = func() {
					onRun()
					ht.fake.Containers[0].State = container.StateExited
					ht.fake.ContainerDetails["a2"] = container.InspectResponse{ID: "a2", State: &container.State{Status: container.StateExited, ExitCode: 3}}
				}
			},
			code: 502, status: "failed", err: "deploying: service app: a container exited with code 3",
			calls: []string{"up web pull=true wait=1m0s"}, env: envV1,
		},
		{
			name: "sync fails", health: `{"wait_seconds":60}`,
			setup: func(ht *healthTest) { ht.runner.syncErr = errors.New("agent gone") },
			code:  502, status: "failed", env: envV1,
			check: func(t *testing.T, ht *healthTest, _ deploymentRunJSON) {
				// The stack was released, and its next start syncs the values that worked.
				ht.runner.mu.Lock()
				ht.runner.syncErr = nil
				ht.runner.mu.Unlock()
				if code, body := ht.do(t, "POST", "local", "/web/up", ""); code != http.StatusOK {
					t.Fatalf("up after the failed run = %d %s", code, body)
				}
				ht.runner.mu.Lock()
				defer ht.runner.mu.Unlock()
				if got := ht.runner.synced["web"].Env; got != envV1 {
					t.Errorf("agent's .env on the next up = %q", got)
				}
			},
		},
		{
			name: "stays up", health: `{"stable_seconds":30}`,
			code: 200, status: "succeeded", calls: []string{"up web pull=true"}, env: envV2,
			check: func(t *testing.T, _ *healthTest, run deploymentRunJSON) {
				if run.Step != "checking" {
					t.Errorf("step = %q, want checking", run.Step)
				}
			},
		},
		{
			name: "restarts during the window", health: `{"stable_seconds":30}`,
			setup: func(ht *healthTest) {
				ht.onWindow = func() {
					ht.fake.ContainerDetails["a2"] = container.InspectResponse{ID: "a2", RestartCount: 1, State: &container.State{Status: container.StateRunning, Running: true}}
				}
			},
			code: 502, status: "failed", err: "checking: service app: a container restarted during the stability window",
			calls: []string{"up web pull=true"}, env: envV1,
		},
		{
			name: "http check passes", health: `{"http":{"service":"app","port":8080,"path":"/health"}}`,
			code: 200, status: "succeeded", calls: []string{"up web pull=true"}, env: envV2,
			check: func(t *testing.T, ht *healthTest, _ deploymentRunJSON) {
				want := probe.Request{Container: "a2", Port: 8080, Path: "/health", Status: 200, Timeout: 5 * time.Second}
				if len(ht.runner.probes) != 1 || ht.runner.probes[0] != want {
					t.Errorf("probes = %+v, want %+v", ht.runner.probes, want)
				}
			},
		},
		{
			name: "http check on a service without a container", health: `{"http":{"service":"worker","port":8080}}`,
			code: 502, status: "failed", err: "checking: service worker has no running container for the HTTP check",
			calls: []string{"up web pull=true"}, env: envV1,
		},
		{
			name: "agent too old for http checks", health: `{"http":{"service":"app","port":8080}}`,
			setup: func(ht *healthTest) { ht.runner.probeErr = stacks.ErrNoProbe },
			code:  200, status: "succeeded", calls: []string{"up web pull=true"}, env: envV2,
			check: func(t *testing.T, _ *healthTest, run deploymentRunJSON) {
				if len(run.Warnings) != 1 || !strings.Contains(run.Warnings[0], "too old for HTTP checks") {
					t.Errorf("warnings = %q", run.Warnings)
				}
			},
		},
		{
			name: "http check fails without rollback", health: `{"http":{"service":"app","port":8080}}`,
			setup: func(ht *healthTest) { ht.runner.probeErr = &probe.FailedError{Msg: "answered another status than 200"} },
			code:  502, status: "failed", err: "checking: service app: HTTP check: answered another status than 200",
			calls: []string{"up web pull=true"}, env: envV1,
		},
		{
			name: "http check fails and rolls back", health: `{"http":{"service":"app","port":8080},"rollback":true}`,
			setup: func(ht *healthTest) { ht.runner.probeErr = &probe.FailedError{Msg: "answered another status than 200"} },
			code:  502, status: "rolled_back", err: "checking: service app: HTTP check: answered another status than 200; rolled back to the previous values",
			calls: []string{"up web pull=true", "up web pull=true"}, env: envV1,
			check: func(t *testing.T, ht *healthTest, run deploymentRunJSON) {
				if run.Step != "rolling_back" {
					t.Errorf("step = %q, want rolling_back", run.Step)
				}
				ht.runner.mu.Lock()
				defer ht.runner.mu.Unlock()
				if got := ht.runner.synced["web"].Env; got != envV1 {
					t.Errorf("agent's .env after the rollback = %q", got)
				}
			},
		},
		{
			name: "rollback fails too", health: `{"http":{"service":"app","port":8080},"rollback":true}`,
			setup: func(ht *healthTest) {
				ht.runner.probeErr = &probe.FailedError{Msg: "answered another status than 200"}
				ht.runner.upErrs = []error{nil, stacks.ErrNoCompose}
			},
			code: 502, status: "failed", err: "the rollback failed too: docker-compose is not installed",
			calls: []string{"up web pull=true", "up web pull=true"}, env: envV1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ht := newHealthTest(t, tt.health)
			if tt.setup != nil {
				tt.setup(ht)
			}
			code, run := ht.deploy(t)
			if code != tt.code || run.Status != tt.status || !strings.Contains(run.Error, tt.err) {
				t.Errorf("deploy = %d %s %q; want %d %s %q", code, run.Status, run.Error, tt.code, tt.status, tt.err)
			}
			ht.runner.mu.Lock()
			calls := ht.runner.calls
			ht.runner.mu.Unlock()
			if strings.Join(calls, "; ") != strings.Join(tt.calls, "; ") {
				t.Errorf("runner calls = %q, want %q", calls, tt.calls)
			}
			if got := ht.env(t); got != tt.env {
				t.Errorf(".env = %q, want %q", got, tt.env)
			}
			if tt.check != nil {
				tt.check(t, ht, run)
			}
		})
	}
}

func TestDeployHealthSettings(t *testing.T) {
	t.Parallel()
	st := newDeploymentTest(t)
	rec := do(t, st.h, request{method: "PATCH", path: "/api/v1/deployments/shop", cookie: st.cookie,
		body: `{"health":{"wait_seconds":90,"http":{"service":"app","port":80},"rollback":true}}`})
	want := `"health":{"wait_seconds":90,"stable_seconds":0,"http":{"service":"app","port":80,"path":"/","status":200,"timeout_seconds":5},"rollback":true}`
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
		t.Errorf("set health = %d %s", rec.Code, rec.Body)
	}
	rec = do(t, st.h, request{method: "PATCH", path: "/api/v1/deployments/shop", cookie: st.cookie, body: `{"health":{"http":{"service":"app","port":0}}}`})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "http.port") {
		t.Errorf("bad port = %d %s", rec.Code, rec.Body)
	}
}

func TestDeployStream(t *testing.T) {
	t.Parallel()
	ht := newHealthTest(t, `{"http":{"service":"app","port":8080}}`)
	ht.runner.lines = []stacks.Line{{Stream: "stderr", Text: " app Pulled"}, {Stream: "stderr", Text: " Container web-app-1  Started"}}
	ht.runner.probeErr = stacks.ErrNoProbe
	rec := do(t, ht.h, request{method: "POST", path: "/api/v1/deployments/shop/deploys?stream=1", body: `{"variables":{"API_IMAGE":"app:v2"}}`, headers: bearer(ht.token)})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("stream = %d %s", rec.Code, rec.Header())
	}
	var events []deployEvent
	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		var ev deployEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		events = append(events, ev)
	}
	// The run may get ahead of the stream, so steps can be missed, never reordered.
	if first := events[0]; first.Run == 0 || first.Message != "queued" {
		t.Errorf("first line = %+v", first)
	}
	order := map[string]int{"pulling": 1, "deploying": 2, "checking": 3}
	last := 0
	for _, ev := range events {
		if n := order[ev.Step]; ev.Status == "" && n != 0 {
			if n < last {
				t.Errorf("step %s after a later one; events %+v", ev.Step, events)
			}
			last = n
		}
	}
	if end := events[len(events)-1]; end.Status != "succeeded" || end.Run == 0 || end.Step != "checking" || len(end.Warnings) != 1 {
		t.Errorf("final line = %+v", end)
	}

	// Errors before the run starts are ordinary JSON errors.
	rec = do(t, ht.h, request{method: "POST", path: "/api/v1/deployments/shop/deploys?stream=1", body: `{"variables":{"OTHER":"x"}}`, headers: bearer(ht.token)})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad variable = %d %s", rec.Code, rec.Body)
	}
}

func TestServiceStates(t *testing.T) {
	t.Parallel()
	running := containerState{service: "web", state: container.StateRunning}
	tests := []struct {
		name  string
		start serviceStates
		end   serviceStates
		want  string
	}{
		{"all up", serviceStates{"a": running}, serviceStates{"a": running}, ""},
		{"one-off job done", nil, serviceStates{"a": running, "j": {service: "migrate", state: container.StateExited}}, ""},
		{"job failed", nil, serviceStates{"j": {service: "migrate", state: container.StateExited, exitCode: 1}}, "service migrate: a container exited with code 1"},
		{"restarting", nil, serviceStates{"a": {service: "web", state: container.StateRestarting}}, "service web: a container is restarting"},
		{"unhealthy", nil, serviceStates{"a": {service: "web", state: container.StateRunning, unhealthy: true}}, "service web: a container is unhealthy"},
		{"gone", serviceStates{"a": running}, serviceStates{}, "service web: a container went away"},
		{"restarted", serviceStates{"a": running}, serviceStates{"a": {service: "web", state: container.StateRunning, restarts: 2}}, "service web: a container restarted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.end.changedSince(tt.start)
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Errorf("changedSince = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeployOutput(t *testing.T) {
	t.Parallel()
	var steps []string
	out := deployOutput(func(s deployments.Step) { steps = append(steps, string(s)) })
	for _, l := range []string{" app Pulling", " app Pulled", " Network web_default  Created", " Container web-app-1  Started"} {
		out(stacks.Line{Text: l})
	}
	if strings.Join(steps, ",") != "deploying" {
		t.Errorf("steps = %q, want one switch to deploying", steps)
	}
}
