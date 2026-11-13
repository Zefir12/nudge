package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/gitstacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/gitstacks/gitstackstest"
)

const gitCompose = "services:\n  app:\n    image: nginx:1\n"

type gitStackTest struct {
	stackTest
	repo *gitstackstest.Repo
	svc  *gitstacks.Service
	// first is the commit the stack web was created at.
	first string
}

// newGitStackTest has stack web from a repository with compose.yaml and app.env, its .env
// holding a secret only nudge has.
func newGitStackTest(t *testing.T, mods ...func(*Options)) gitStackTest {
	t.Helper()
	gt := gitStackTest{repo: gitstackstest.New(t)}
	gt.first = gt.repo.Commit(map[string]string{"compose.yaml": gitCompose, "app.env": "FROM_REPO=1\n"})
	mods = append(mods, func(o *Options) { gt.svc = o.GitStacks })
	gt.stackTest = newStackTest(t, mods...)
	body := fmt.Sprintf(`{"name":"web","env":"SECRET=keep\n","git":{"repo_url":%q,"compose_path":"compose.yaml","env_path":"app.env"}}`, gt.repo.URL)
	if c, out := gt.do(t, "POST", "local", "", body); c != http.StatusCreated {
		t.Fatalf("create = %d %s", c, out)
	}
	return gt
}

func (gt gitStackTest) get(t *testing.T) stackResponse {
	t.Helper()
	c, out := gt.do(t, "GET", "local", "/web", "")
	if c != http.StatusOK {
		t.Fatalf("get = %d %s", c, out)
	}
	var resp stackResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Git == nil {
		t.Fatalf("no git source: %s", out)
	}
	return resp
}

func TestCreateStackFromGit(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	got := gt.get(t)
	if got.Compose != gitCompose || got.Env != "SECRET=keep\nFROM_REPO=1\n" {
		t.Fatalf("files = %q, %q", got.Compose, got.Env)
	}
	if got.Git.Commit != gt.first || got.Git.ComposePath != "compose.yaml" || got.Git.LastError != "" {
		t.Fatalf("git = %+v", got.Git)
	}
	tests := []struct {
		name, body string
		code       int
		want       string
	}{
		{"taken name", fmt.Sprintf(`{"name":"web","git":{"repo_url":%q,"compose_path":"compose.yaml"}}`, gt.repo.URL), 409, "exists"},
		{"bad source", `{"name":"b","git":{"repo_url":"git://x/y","compose_path":"c"}}`, 400, "must be https"},
		{"missing file", fmt.Sprintf(`{"name":"c","git":{"repo_url":%q,"compose_path":"nope.yaml"}}`, gt.repo.URL), 502, "nope.yaml is not in the repository"},
		{"missing repo", `{"name":"d","git":{"repo_url":"file:///no/such/repo","compose_path":"c"}}`, 502, "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, out := gt.do(t, "POST", "local", "", tt.body)
			if c != tt.code || !strings.Contains(out, tt.want) {
				t.Fatalf("= %d %s, want %d %q", c, out, tt.code, tt.want)
			}
		})
	}
	// A failed create leaves no stack behind.
	for _, name := range []string{"b", "c", "d"} {
		if _, err := os.Stat(filepath.Join(gt.dir, strconv.FormatInt(gt.ids["local"], 10), name)); !os.IsNotExist(err) {
			t.Fatalf("stack %s left behind: %v", name, err)
		}
	}
}

func TestGitStackComposeIsReadOnly(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	body, _ := json.Marshal(map[string]string{"compose": "services: {}\n", "env": "A=1\n"})
	if c, out := gt.do(t, "PUT", "local", "/web", string(body)); c != http.StatusConflict || !strings.Contains(out, "comes from git") {
		t.Fatalf("edit compose = %d %s", c, out)
	}
	body, _ = json.Marshal(map[string]string{"compose": gitCompose, "env": "A=1\n"})
	if c, out := gt.do(t, "PUT", "local", "/web", string(body)); c != http.StatusNoContent {
		t.Fatalf("edit env = %d %s", c, out)
	}
	if got := gt.get(t); got.Env != "A=1\n" {
		t.Fatalf("env = %q", got.Env)
	}
	// Detached, it is a stack like any other and keeps its files.
	if c, out := gt.do(t, "DELETE", "local", "/web/git", ""); c != http.StatusNoContent {
		t.Fatalf("detach = %d %s", c, out)
	}
	c, out := gt.do(t, "DELETE", "local", "/web/git", "")
	if c != http.StatusNotFound {
		t.Fatalf("detach twice = %d %s", c, out)
	}
	if c, out := gt.do(t, "POST", "local", "/web/git/pull", ""); c != http.StatusConflict {
		t.Fatalf("pull a detached stack = %d %s", c, out)
	}
	body, _ = json.Marshal(map[string]string{"compose": "services: {}\n"})
	if c, out := gt.do(t, "PUT", "local", "/web", string(body)); c != http.StatusNoContent {
		t.Fatalf("edit detached = %d %s", c, out)
	}
}

func TestPullStackGit(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	next := gt.repo.Commit(map[string]string{"compose.yaml": gitCompose + "# v2\n", "app.env": "FROM_REPO=2\n"})
	c, out := gt.do(t, "POST", "local", "/web/git/pull", "")
	if c != http.StatusOK || !strings.Contains(out, "pulled commit "+next[:12]) {
		t.Fatalf("pull = %d %s", c, out)
	}
	if evs := stackEvents(t, out); evs[len(evs)-1].ExitCode == nil || *evs[len(evs)-1].ExitCode != 0 {
		t.Fatalf("events = %+v", evs)
	}
	got := gt.get(t)
	if got.Git.Commit != next || got.Compose != gitCompose+"# v2\n" || got.Env != "SECRET=keep\nFROM_REPO=2\n" {
		t.Fatalf("after pull: %+v %q %q", got.Git, got.Compose, got.Env)
	}
	if synced := gt.runner.synced["web"]; synced.Compose != got.Compose || synced.Env != got.Env {
		t.Fatalf("synced = %+v", synced)
	}
	if want := "up web pull=true"; len(gt.runner.calls) != 1 || gt.runner.calls[0] != want {
		t.Fatalf("calls = %v", gt.runner.calls)
	}
}

func TestPullStackGitFailures(t *testing.T) {
	t.Parallel()
	t.Run("a failed deploy puts the files back", func(t *testing.T) {
		t.Parallel()
		gt := newGitStackTest(t)
		bad := gt.repo.Commit(map[string]string{"compose.yaml": gitCompose + "# bad\n", "app.env": "FROM_REPO=bad\n"})
		gt.runner.code = 1
		c, out := gt.do(t, "POST", "local", "/web/git/pull", "")
		if c != http.StatusOK || !strings.Contains(out, "stay at the previous commit") {
			t.Fatalf("pull = %d %s", c, out)
		}
		got := gt.get(t)
		if got.Git.Commit != gt.first || got.Compose != gitCompose || strings.Contains(got.Env, "bad") {
			t.Fatalf("after a failed deploy: %+v %q %q", got.Git, got.Compose, got.Env)
		}
		if !strings.Contains(got.Git.LastError, bad[:12]) {
			t.Fatalf("last error = %q", got.Git.LastError)
		}
	})
	t.Run("an unreadable repository deploys nothing", func(t *testing.T) {
		t.Parallel()
		gt := newGitStackTest(t)
		if err := os.RemoveAll(gt.repo.Dir); err != nil {
			t.Fatal(err)
		}
		c, out := gt.do(t, "POST", "local", "/web/git/pull", "")
		if c != http.StatusBadGateway || !strings.Contains(out, "not found") {
			t.Fatalf("pull = %d %s", c, out)
		}
		got := gt.get(t)
		if len(gt.runner.calls) != 0 || got.Git.LastError == "" || got.Compose != gitCompose {
			t.Fatalf("calls %v, source %+v", gt.runner.calls, got.Git)
		}
		// Plain up still deploys the files it has.
		if c, out := gt.do(t, "POST", "local", "/web/up", ""); c != http.StatusOK {
			t.Fatalf("up = %d %s", c, out)
		}
	})
}

func TestSetStackGit(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	other := gitstackstest.New(t)
	commit := other.Commit(map[string]string{"sub/c.yaml": "services:\n  other:\n    image: x\n"})
	body := fmt.Sprintf(`{"repo_url":%q,"compose_path":"sub/c.yaml","poll_seconds":120}`, other.URL)
	c, out := gt.do(t, "PUT", "local", "/web/git", body)
	if c != http.StatusOK || strings.Contains(out, "secret") {
		t.Fatalf("set = %d %s", c, out)
	}
	got := gt.get(t)
	if got.Git.Commit != commit || got.Git.PollSeconds != 120 || !strings.Contains(got.Compose, "other") {
		t.Fatalf("after set: %+v %q", got.Git, got.Compose)
	}
	if c, out := gt.do(t, "PUT", "local", "/nope/git", body); c != http.StatusNotFound {
		t.Fatalf("set on unknown = %d %s", c, out)
	}
	// A plain stack can start following a repository.
	if c, out := gt.do(t, "POST", "local", "", `{"name":"plain","compose":"services: {}\n"}`); c != http.StatusCreated {
		t.Fatalf("create = %d %s", c, out)
	}
	if c, out := gt.do(t, "PUT", "local", "/plain/git", body); c != http.StatusOK {
		t.Fatalf("set on plain = %d %s", c, out)
	}
}

func TestSecretNeverAnswered(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	const secret = "tok-very-secret"
	body := fmt.Sprintf(`{"repo_url":"https://git.invalid/a.git","compose_path":"c.yaml","auth":"token","secret":%q}`, secret)
	if c, out := gt.do(t, "PUT", "local", "/web/git", body); strings.Contains(out, secret) || c == http.StatusOK {
		t.Fatalf("set = %d %s", c, out)
	}
	if _, out := gt.do(t, "GET", "local", "/web", ""); strings.Contains(out, secret) {
		t.Fatalf("get = %s", out)
	}
}

func TestRemoveStackForgetsSource(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	gt.fake.Containers = nil
	if c, out := gt.do(t, "DELETE", "local", "/web", ""); c != http.StatusNoContent {
		t.Fatalf("remove = %d %s", c, out)
	}
	if _, err := gt.svc.Get(context.Background(), gt.ids["local"], "web"); !errors.Is(err, gitstacks.ErrNotFound) {
		t.Fatalf("source after remove: %v", err)
	}
}

func TestDeployHookPullsGitStack(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	hook := createHook(t, gt.h, gt.cookie, hooksPath(gt.envTest, "local"), `{"name":"ci","stack":"web"}`)
	next := gt.repo.Commit(map[string]string{"compose.yaml": gitCompose + "# hook\n"})
	rec := trigger(t, gt.h, hook.ID, "?wait=1", "", bearer(hook.Secret))
	if run := runOf(t, rec); rec.Code >= 400 || run.Status != "updated" {
		t.Fatalf("trigger = %d %+v", rec.Code, run)
	}
	if got := gt.get(t); got.Git.Commit != next || !strings.Contains(got.Compose, "# hook") {
		t.Fatalf("after hook: %+v %q", got.Git, got.Compose)
	}
}

func TestDeploymentPullsGitStack(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	rec := do(t, gt.h, request{method: "POST", path: "/api/v1/deployments", cookie: gt.cookie,
		body: fmt.Sprintf(`{"name":"shop","environment":%d,"stack":"web","variables":["API_IMAGE"]}`, gt.ids["local"])})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create deployment = %d %s", rec.Code, rec.Body)
	}
	deploy := func() deploymentRunJSON {
		t.Helper()
		token := deployToken(t, gt.stackTest, "ci"+strconv.Itoa(len(gt.runner.calls)), "deploy")
		rec := do(t, gt.h, request{method: "POST", path: "/api/v1/deployments/shop/deploys?wait=1",
			headers: bearer(token), body: `{"variables":{"API_IMAGE":"app:v2"}}`})
		return deployRunOf(t, rec.Body.String())
	}
	next := gt.repo.Commit(map[string]string{"compose.yaml": gitCompose + "# ci\n"})
	if run := deploy(); run.Status != "succeeded" {
		t.Fatalf("run = %+v", run)
	}
	got := gt.get(t)
	if got.Git.Commit != next || !strings.Contains(got.Compose, "# ci") || !strings.Contains(got.Env, "API_IMAGE=app:v2") || !strings.Contains(got.Env, "SECRET=keep") {
		t.Fatalf("after deploy: %+v %q %q", got.Git, got.Compose, got.Env)
	}
	// A deploy that fails goes back to the files that worked, commit included.
	gt.repo.Commit(map[string]string{"compose.yaml": gitCompose + "# broken\n"})
	gt.runner.code = 1
	if run := deploy(); run.Status != "failed" {
		t.Fatalf("run = %+v", run)
	}
	after := gt.get(t)
	if after.Git.Commit != next || strings.Contains(after.Compose, "broken") || after.Git.LastError == "" {
		t.Fatalf("after a failed deploy: %+v %q", after.Git, after.Compose)
	}
}

func TestPollDeploysAMovedRepository(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	ctx := context.Background()
	source := func() gitstacks.Source {
		src, err := gt.svc.Get(ctx, gt.ids["local"], "web")
		if err != nil {
			t.Fatal(err)
		}
		return src
	}
	gt.svc.Check(ctx, source())
	if len(gt.runner.calls) != 0 {
		t.Fatalf("deployed an unchanged repository: %v", gt.runner.calls)
	}
	next := gt.repo.Commit(map[string]string{"compose.yaml": gitCompose + "# polled\n"})
	gt.svc.Check(ctx, source())
	if len(gt.runner.calls) != 1 || source().Commit != next {
		t.Fatalf("calls %v, commit %s, want %s", gt.runner.calls, source().Commit, next)
	}
	// A bad commit is tried once.
	gt.repo.Commit(map[string]string{"compose.yaml": gitCompose + "# bad\n"})
	gt.runner.code = 1
	gt.svc.Check(ctx, source())
	gt.svc.Check(ctx, source())
	if len(gt.runner.calls) != 2 || source().Commit != next || source().LastError == "" {
		t.Fatalf("calls %v, source %+v", gt.runner.calls, source())
	}
}

func TestStartingAPlainStackWritesNothing(t *testing.T) {
	t.Parallel()
	st := newStackTest(t)
	if c, out := st.do(t, "POST", "local", "", `{"name":"web","compose":"services: {}\n"}`); c != http.StatusCreated {
		t.Fatalf("create = %d %s", c, out)
	}
	file := filepath.Join(st.dir, strconv.FormatInt(st.ids["local"], 10), "web", "compose.yaml")
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	hook := createHook(t, st.h, st.cookie, hooksPath(st.envTest, "local"), `{"name":"ci","stack":"web"}`)
	if rec := trigger(t, st.h, hook.ID, "?wait=1", "", bearer(hook.Secret)); rec.Code >= 400 {
		t.Fatalf("trigger = %d %s", rec.Code, rec.Body)
	}
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("the deploy replaced the unchanged compose file")
	}
}

func TestPullThatCannotSyncIsReported(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	gt.repo.Commit(map[string]string{"compose.yaml": gitCompose + "# new\n"})
	gt.runner.syncErr = errors.New("agent gone")
	if c, out := gt.do(t, "POST", "local", "/web/git/pull", ""); c < 400 {
		t.Fatalf("pull = %d %s", c, out)
	}
	got := gt.get(t)
	if got.Git.Commit != gt.first || got.Compose != gitCompose || got.Git.LastError == "" {
		t.Fatalf("after a failed sync: %+v %q", got.Git, got.Compose)
	}
	// It wasn't the commit's fault: polling tries again.
	src, err := gt.svc.Get(context.Background(), gt.ids["local"], "web")
	if err != nil || src.SeenCommit != gt.first {
		t.Fatalf("seen = %q, %v", src.SeenCommit, err)
	}
}

func TestDeploymentPreviousIsThePreRepositoryValue(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	gt.repo.Commit(map[string]string{"app.env": "API_IMAGE=from-repo\n"})
	body, _ := json.Marshal(map[string]string{"compose": gitCompose, "env": "API_IMAGE=before\n"})
	if c, out := gt.do(t, "PUT", "local", "/web", string(body)); c != http.StatusNoContent {
		t.Fatalf("edit env = %d %s", c, out)
	}
	rec := do(t, gt.h, request{method: "POST", path: "/api/v1/deployments", cookie: gt.cookie,
		body: fmt.Sprintf(`{"name":"shop","environment":%d,"stack":"web","variables":["API_IMAGE"]}`, gt.ids["local"])})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create deployment = %d %s", rec.Code, rec.Body)
	}
	rec = do(t, gt.h, request{method: "POST", path: "/api/v1/deployments/shop/deploys?wait=1", cookie: gt.cookie, body: `{"variables":{"API_IMAGE":"app:v2"}}`})
	if run := deployRunOf(t, rec.Body.String()); run.Status != "succeeded" || run.Previous["API_IMAGE"] != "before" {
		t.Fatalf("run = %+v", run)
	}
}

func TestStackEnvExample(t *testing.T) {
	t.Parallel()
	gt := newGitStackTest(t)
	next := gt.repo.Commit(map[string]string{"compose.yaml": gitCompose, "app.env": "# the port\nPORT=80\nFROM_REPO=2\n"})
	c, out := gt.do(t, "GET", "local", "/web/git/env-example", "")
	if c != http.StatusOK {
		t.Fatalf("get = %d %s", c, out)
	}
	var got stackEnvExampleResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	want := stackEnvExampleResponse{Path: "app.env", Commit: next, Content: "# the port\nPORT=80\nFROM_REPO=2\n"}
	if got != want {
		t.Fatalf("example = %+v, want %+v", got, want)
	}
	// Reading it changes nothing: the stack stays at its commit with its .env.
	if s := gt.get(t); s.Git.Commit != gt.first || s.Env != "SECRET=keep\nFROM_REPO=1\n" {
		t.Fatalf("after read: %q at %s", s.Env, s.Git.Commit)
	}

	if c, out := gt.do(t, "POST", "local", "", `{"name":"plain","compose":"services: {}\n"}`); c != http.StatusCreated {
		t.Fatalf("create = %d %s", c, out)
	}
	if c, out := gt.do(t, "POST", "local", "", fmt.Sprintf(`{"name":"noenv","git":{"repo_url":%q,"compose_path":"compose.yaml"}}`, gt.repo.URL)); c != http.StatusCreated {
		t.Fatalf("create noenv = %d %s", c, out)
	}
	tests := []struct {
		name, path string
		code       int
		want       string
	}{
		{"plain stack", "/plain/git/env-example", 404, "no git source"},
		{"no env_path", "/noenv/git/env-example", 404, "no env file"},
		{"unknown stack", "/nope/git/env-example", 404, "no git source"},
		{"bad name", "/BAD/git/env-example", 404, "stack not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, out := gt.do(t, "GET", "local", tt.path, "")
			if c != tt.code || !strings.Contains(out, tt.want) {
				t.Fatalf("= %d %s, want %d %q", c, out, tt.code, tt.want)
			}
		})
	}

}
