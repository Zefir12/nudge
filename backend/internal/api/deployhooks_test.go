package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/deployhooks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel"
)

// newDeployHookTest is an environment "local" running the container web (oldWeb) on
// sha256:img1, whose image nginx:1.29 is pulled is as pulled.
func newDeployHookTest(t *testing.T, pulled string) (envTest, *dockertest.Fake) {
	t.Helper()
	ct := oldWeb()
	images := map[string]image.InspectResponse{
		"sha256:img1": {ID: "sha256:img1", RepoDigests: []string{"nginx@sha256:old"}},
		"sha256:img2": {ID: "sha256:img2", RepoDigests: []string{"nginx@sha256:new"}},
	}
	images["nginx:1.29"] = images[pulled]
	fake := &dockertest.Fake{Resources: dockertest.Resources{
		ContainerDetails: map[string]container.InspectResponse{ct.ID: ct, "web": ct}, ImageDetails: images,
	}}
	return newEnvTest(t, map[string]*dockertest.Fake{"local": fake}), fake
}

func hooksPath(et envTest, env string) string {
	return fmt.Sprintf("/api/v1/environments/%d/webhooks", et.ids[env])
}

func createHook(t *testing.T, h http.Handler, cookie *http.Cookie, path, body string) deployHookSecretJSON {
	t.Helper()
	rec := do(t, h, request{method: "POST", path: path, body: body, cookie: cookie})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST %s %s = %d %s", path, body, rec.Code, rec.Body)
	}
	var out deployHookSecretJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func signBody(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func trigger(t *testing.T, h http.Handler, id, query, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, request{method: "POST", path: "/api/v1/hooks/" + id + query, body: body, headers: headers})
}

func runOf(t *testing.T, rec *httptest.ResponseRecorder) deployHookRunJSON {
	t.Helper()
	var run deployHookRunJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("run: %v (%s)", err, rec.Body)
	}
	return run
}

func TestDeployHookManagement(t *testing.T) {
	t.Parallel()
	et, _ := newDeployHookTest(t, "sha256:img2")
	path := hooksPath(et, "local")

	// By ID, stored by name.
	hook := createHook(t, et.h, et.cookie, path, `{"name":"ci","container":"abc123def4567890","tag_filter":"1.*"}`)
	if hook.Kind != "container" || hook.Target != "web" || hook.TagFilter != "1.*" || hook.CreatedBy != "admin" ||
		!strings.HasPrefix(hook.Secret, deployhooks.SecretPrefix) || hook.URL != "http://example.com/api/v1/hooks/"+hook.ID {
		t.Errorf("created = %+v", hook)
	}

	errorsTests := []struct {
		name, body string
		code       int
		want       string
	}{
		{name: "no target", body: `{"name":"x"}`, code: http.StatusBadRequest, want: "name a container or a stack"},
		{name: "both targets", body: `{"name":"x","container":"web","stack":"app"}`, code: http.StatusBadRequest, want: "name a container or a stack"},
		{name: "unknown container", body: `{"name":"x","container":"nope"}`, code: http.StatusNotFound, want: "container not found"},
		{name: "invalid container", body: `{"name":"x","container":"../x"}`, code: http.StatusNotFound, want: "container not found"},
		{name: "unknown stack", body: `{"name":"x","stack":"app"}`, code: http.StatusNotFound, want: "stack not found"},
		{name: "taken name", body: `{"name":"ci","container":"web"}`, code: http.StatusConflict, want: "already exists"},
		{name: "bad name", body: `{"name":"","container":"web"}`, code: http.StatusBadRequest, want: "name must be"},
		{name: "bad filter", body: `{"name":"x","container":"web","tag_filter":"["}`, code: http.StatusBadRequest, want: "not a valid pattern"},
	}
	for _, tt := range errorsTests {
		rec := do(t, et.h, request{method: "POST", path: path, body: tt.body, cookie: et.cookie})
		if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("%s: POST = %d %s, want %d saying %q", tt.name, rec.Code, rec.Body, tt.code, tt.want)
		}
	}

	list := func(query string) []deployHookJSON {
		t.Helper()
		rec := do(t, et.h, request{method: "GET", path: path + query, cookie: et.cookie})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET%s = %d %s", query, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), hook.Secret) {
			t.Errorf("list shows the secret: %s", rec.Body)
		}
		var out []deployHookJSON
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := list(""); len(got) != 1 || got[0].ID != hook.ID {
		t.Errorf("list = %+v", got)
	}
	if got := list("?container=web"); len(got) != 1 {
		t.Errorf("?container=web = %+v", got)
	}
	if got := list("?stack=web"); len(got) != 0 {
		t.Errorf("?stack=web = %+v, want none", got)
	}
	if rec := do(t, et.h, request{method: "GET", path: path + "?stack=a&container=b", cookie: et.cookie}); rec.Code != http.StatusBadRequest {
		t.Errorf("both filters = %d", rec.Code)
	}

	rec := do(t, et.h, request{method: "POST", path: path + "/" + hook.ID + "/secret", cookie: et.cookie})
	var rotated deployHookSecretJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &rotated); rec.Code != http.StatusOK || err != nil || rotated.Secret == hook.Secret || rotated.ID != hook.ID {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body)
	}
	if rec := trigger(t, et.h, hook.ID, "", "", bearer(hook.Secret)); rec.Code != http.StatusUnauthorized {
		t.Errorf("old secret = %d, want 401", rec.Code)
	}

	rec = do(t, et.h, request{method: "GET", path: path + "/" + hook.ID + "/runs", cookie: et.cookie})
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("runs = %d %s, want []", rec.Code, rec.Body)
	}
	if rec := do(t, et.h, request{method: "DELETE", path: path + "/" + hook.ID, cookie: et.cookie}); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE = %d %s", rec.Code, rec.Body)
	}
	for _, req := range []request{
		{method: "DELETE", path: path + "/" + hook.ID},
		{method: "POST", path: path + "/" + hook.ID + "/secret"},
		{method: "GET", path: path + "/" + hook.ID + "/runs"},
	} {
		req.cookie = et.cookie
		if rec := do(t, et.h, req); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s after delete = %d, want 404", req.method, req.path, rec.Code)
		}
	}
	if rec := trigger(t, et.h, hook.ID, "", "", bearer(rotated.Secret)); rec.Code != http.StatusUnauthorized {
		t.Errorf("deleted webhook = %d, want 401", rec.Code)
	}
	// Management needs a session or a token.
	if rec := do(t, et.h, request{method: "GET", path: path}); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET without a session = %d", rec.Code)
	}
}

func TestDeployHookOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &dockertest.Fake{Resources: dockertest.Resources{ContainerDetails: map[string]container.InspectResponse{"web": oldWeb()}}}
	h, svc, db := newTestHandlerOpts(t, func(environments.Environment) (docker.Client, error) { return fake, nil }, func(o *Options) {
		o.IsAdmin = func(u auth.User) bool { return u.Username == "admin" }
	})
	admin, err := svc.CreateFirstUser(ctx, "admin", "longenough")
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at, updated_at) VALUES ('bob', 'x', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	bobID, _ := res.LastInsertId()
	res, err = db.ExecContext(ctx, `INSERT INTO environments (name, kind, created_at) VALUES ('local', 'local', 0)`)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := res.LastInsertId()
	_, adminToken, err := svc.CreateToken(ctx, admin.ID, "ci")
	if err != nil {
		t.Fatal(err)
	}
	_, bobToken, err := svc.CreateToken(ctx, bobID, "ci")
	if err != nil {
		t.Fatal(err)
	}
	as := func(token string, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return do(t, h, request{method: method, path: path, body: body, headers: bearer(token)})
	}
	path := fmt.Sprintf("/api/v1/environments/%d/webhooks", env)
	decode := func(rec *httptest.ResponseRecorder) deployHookSecretJSON {
		t.Helper()
		var out deployHookSecretJSON
		if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("create = %d %s", rec.Code, rec.Body)
		}
		return out
	}
	adminHook := decode(as(adminToken, "POST", path, `{"name":"deploy","container":"web"}`))
	bobHook := decode(as(bobToken, "POST", path, `{"name":"deploy","container":"web"}`))

	count := func(token string) int {
		t.Helper()
		var out []deployHookJSON
		if err := json.Unmarshal(as(token, "GET", path, "").Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return len(out)
	}
	if n := count(bobToken); n != 1 {
		t.Errorf("bob sees %d webhooks, want his own", n)
	}
	if n := count(adminToken); n != 2 {
		t.Errorf("admin sees %d webhooks, want both", n)
	}
	for _, req := range []struct{ method, path string }{
		{"GET", path + "/" + adminHook.ID + "/runs"},
		{"POST", path + "/" + adminHook.ID + "/secret"},
		{"DELETE", path + "/" + adminHook.ID},
	} {
		if rec := as(bobToken, req.method, req.path, ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "webhook not found") {
			t.Errorf("bob %s %s = %d %s, want 404", req.method, req.path, rec.Code, rec.Body)
		}
	}
	if rec := as(adminToken, "POST", path+"/"+bobHook.ID+"/secret", ""); rec.Code != http.StatusOK {
		t.Errorf("admin rotates bob's = %d", rec.Code)
	}
	if rec := as(adminToken, "DELETE", path+"/"+bobHook.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("admin deletes bob's = %d", rec.Code)
	}
	// Deleting a user deletes their webhooks.
	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if rec := trigger(t, h, adminHook.ID, "", "", bearer(adminHook.Secret)); rec.Code != http.StatusUnauthorized {
		t.Errorf("trigger of a deleted user's webhook = %d, want 401", rec.Code)
	}
}

func TestTriggerDeployHook(t *testing.T) {
	t.Parallel()
	pkg := func(version string) string {
		return `{"action":"created","package":{"name":"nginx","version":"` + version + `"}}`
	}
	tests := []struct {
		name string
		// headers gets the secrets: {secret} is the webhook's, {other} another webhook's,
		// {sig} the webhook's signature of the body.
		headers map[string]string
		id      string
		body    string
		query   string
		code    int
		status  string
		trigger string
	}{
		{name: "bearer", headers: map[string]string{"Authorization": "Bearer {secret}"}, query: "?wait=1", code: http.StatusOK, status: "updated", trigger: "bearer"},
		{name: "forgejo signature", headers: map[string]string{"X-Forgejo-Signature": "{sig}"}, body: `{"ref":"main"}`, query: "?wait=1", code: http.StatusOK, status: "updated", trigger: "signature"},
		{name: "gitea signature", headers: map[string]string{"X-Gitea-Signature": "{sig}"}, body: `{}`, query: "?wait=true", code: http.StatusOK, status: "updated", trigger: "signature"},
		{name: "github signature", headers: map[string]string{"X-Hub-Signature-256": "sha256={sig}"}, body: `{}`, query: "?wait=1", code: http.StatusOK, status: "updated", trigger: "signature"},
		{name: "async", headers: map[string]string{"Authorization": "Bearer {secret}"}, code: http.StatusAccepted, status: "queued"},
		{name: "matching package", headers: map[string]string{"X-Forgejo-Signature": "{sig}", "X-Forgejo-Event": "package"}, body: pkg("1.29.1"), query: "?wait=1", code: http.StatusOK, status: "updated", trigger: "signature"},
		{name: "other package version", headers: map[string]string{"X-Forgejo-Signature": "{sig}", "X-Forgejo-Event": "package"}, body: pkg("2.0"), query: "?wait=1", code: http.StatusOK, status: "skipped"},
		{name: "push event with a filter", headers: map[string]string{"X-Gitea-Signature": "{sig}", "X-Gitea-Event": "push"}, body: `{}`, query: "?wait=1", code: http.StatusOK, status: "updated", trigger: "signature"},
		{name: "bearer package event", headers: map[string]string{"Authorization": "Bearer {secret}", "X-Forgejo-Event": "package"}, body: pkg("2.0"), query: "?wait=1", code: http.StatusOK, status: "updated", trigger: "bearer"},
		{name: "no credential", code: http.StatusUnauthorized},
		{name: "wrong bearer", headers: map[string]string{"Authorization": "Bearer {secret}x"}, code: http.StatusUnauthorized},
		{name: "API token scheme", headers: map[string]string{"Authorization": "Basic {secret}"}, code: http.StatusUnauthorized},
		{name: "unknown webhook", id: "AAAAAAAAAAAAAAAAAAAAAAAAAA", headers: map[string]string{"Authorization": "Bearer {secret}"}, code: http.StatusUnauthorized},
		{name: "signature over another body", headers: map[string]string{"X-Forgejo-Signature": "{sig}"}, body: "tampered", code: http.StatusUnauthorized},
		{name: "signature valid for another webhook", headers: map[string]string{"X-Forgejo-Signature": "{othersig}"}, body: `{}`, code: http.StatusUnauthorized},
		{name: "another webhook's bearer", headers: map[string]string{"Authorization": "Bearer {other}"}, code: http.StatusUnauthorized},
		{name: "bad wait", headers: map[string]string{"Authorization": "Bearer {secret}"}, query: "?wait=maybe", code: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			et, _ := newDeployHookTest(t, "sha256:img2")
			hook := createHook(t, et.h, et.cookie, hooksPath(et, "local"), `{"name":"ci","container":"web","tag_filter":"1.29*"}`)
			other := createHook(t, et.h, et.cookie, hooksPath(et, "local"), `{"name":"other","container":"web"}`)
			body := tt.body
			if tt.name == "signature over another body" {
				body = `{"ref":"main"}`
			}
			headers := map[string]string{}
			for k, v := range tt.headers {
				v = strings.ReplaceAll(v, "{othersig}", signBody(other.Secret, body))
				v = strings.ReplaceAll(v, "{sig}", signBody(hook.Secret, body))
				v = strings.ReplaceAll(v, "{secret}", hook.Secret)
				headers[k] = strings.ReplaceAll(v, "{other}", other.Secret)
			}
			id := hook.ID
			if tt.id != "" {
				id = tt.id
			}
			rec := trigger(t, et.h, id, tt.query, tt.body, headers)
			if rec.Code != tt.code {
				t.Fatalf("trigger = %d %s, want %d", rec.Code, rec.Body, tt.code)
			}
			if tt.code == http.StatusUnauthorized {
				if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"authentication required"}` {
					t.Errorf("body = %s", got)
				}
				entries := auditEntries(t, et.h, et.cookie, "")
				if entries.Entries[0].Action == "POST /api/v1/hooks/{id}" {
					t.Errorf("a refused trigger was audited: %+v", entries.Entries[0])
				}
				return
			}
			if tt.code == http.StatusBadRequest {
				return
			}
			var got struct {
				deployHookRunJSON
				RunID  int64  `json:"run_id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != tt.status {
				t.Errorf("status = %q (%s), want %q", got.Status, rec.Body, tt.status)
			}
			if tt.status == "queued" || tt.status == "skipped" {
				if got.RunID == 0 {
					t.Errorf("no run_id in %s", rec.Body)
				}
			} else if got.Trigger != tt.trigger || got.ImageBefore != "sha256:img1" || got.ImageAfter != "sha256:img2" || got.FinishedAt == nil {
				t.Errorf("run = %+v", got.deployHookRunJSON)
			}

			entries := auditEntries(t, et.h, et.cookie, "")
			e := entries.Entries[0]
			if e.Action != "POST /api/v1/hooks/{id}" || e.User != "admin" || e.Token != "hook:ci" || e.Target != hook.ID ||
				e.Environment != strconv.FormatInt(et.ids["local"], 10) || e.Status != tt.code {
				t.Errorf("audit = %+v", e)
			}
			if tt.status == "updated" {
				hist := history(t, et, "local", "?kind=container&target=web")
				if len(hist) != 1 || hist[0].User != "admin" || hist[0].NewImageID != "sha256:img2" {
					t.Errorf("history = %+v", hist)
				}
			}
			if tt.status == "skipped" {
				runs := do(t, et.h, request{method: "GET", path: hooksPath(et, "local") + "/" + hook.ID + "/runs", cookie: et.cookie})
				if !strings.Contains(runs.Body.String(), `"status":"skipped"`) {
					t.Errorf("runs = %s, want the skipped one", runs.Body)
				}
			}
		})
	}
}

func TestTriggerDeployHookResults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pulled  string
		pullErr error
		code    int
		status  string
		error   string
	}{
		{name: "unchanged", pulled: "sha256:img1", code: http.StatusOK, status: "unchanged"},
		// The daemon's message stays in the log.
		{name: "pull refused", pulled: "sha256:img2", pullErr: errors.New("Error response from daemon: denied: token hunter22 expired"), code: http.StatusBadGateway, status: "failed", error: "pull failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			et, fake := newDeployHookTest(t, tt.pulled)
			fake.PullErr = tt.pullErr
			hook := createHook(t, et.h, et.cookie, hooksPath(et, "local"), `{"name":"ci","container":"web"}`)
			rec := trigger(t, et.h, hook.ID, "?wait=1", "", bearer(hook.Secret))
			run := runOf(t, rec)
			if rec.Code != tt.code || run.Status != tt.status || run.Error != tt.error {
				t.Errorf("trigger = %d %s, want %d %s %q", rec.Code, rec.Body, tt.code, tt.status, tt.error)
			}
			if strings.Contains(rec.Body.String(), "hunter22") || strings.Contains(rec.Body.String(), "denied") {
				t.Errorf("the daemon's message leaked: %s", rec.Body)
			}
			if hist := history(t, et, "local", ""); len(hist) != 0 {
				t.Errorf("history = %+v, want none", hist)
			}
		})
	}
}

func TestTriggerDeployHookRateLimits(t *testing.T) {
	t.Parallel()
	t.Run("failed checks per IP", func(t *testing.T) {
		t.Parallel()
		et, _ := newDeployHookTest(t, "sha256:img2")
		hook := createHook(t, et.h, et.cookie, hooksPath(et, "local"), `{"name":"ci","container":"web"}`)
		for i := range 20 {
			if rec := trigger(t, et.h, hook.ID, "", "", bearer(hook.Secret+"x")); rec.Code != http.StatusUnauthorized {
				t.Fatalf("attempt %d = %d", i, rec.Code)
			}
		}
		// Blocked like failed logins, even with the right secret.
		rec := trigger(t, et.h, hook.ID, "", "", bearer(hook.Secret))
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Errorf("trigger after 20 failures = %d %v", rec.Code, rec.Header())
		}
	})
	t.Run("triggers per webhook", func(t *testing.T) {
		t.Parallel()
		et, _ := newDeployHookTest(t, "sha256:img1")
		hook := createHook(t, et.h, et.cookie, hooksPath(et, "local"), `{"name":"ci","container":"web"}`)
		other := createHook(t, et.h, et.cookie, hooksPath(et, "local"), `{"name":"other","container":"web"}`)
		for i := range deployhooks.TriggerLimit {
			if rec := trigger(t, et.h, hook.ID, "", "", bearer(hook.Secret)); rec.Code != http.StatusAccepted {
				t.Fatalf("trigger %d = %d %s", i, rec.Code, rec.Body)
			}
		}
		rec := trigger(t, et.h, hook.ID, "", "", bearer(hook.Secret))
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" {
			t.Errorf("trigger over the limit = %d %v", rec.Code, rec.Header())
		}
		if rec := trigger(t, et.h, other.ID, "", "", bearer(other.Secret)); rec.Code != http.StatusAccepted {
			t.Errorf("another webhook = %d", rec.Code)
		}
	})
}

func TestTriggerDeployHookStack(t *testing.T) {
	t.Parallel()
	st := newStackTest(t)
	label := func(service string) map[string]string {
		return map[string]string{stacks.ProjectLabel: "web", stacks.ServiceLabel: service}
	}
	st.fake.Containers = []container.Summary{{ID: "a1", Image: "app:v1", ImageID: "sha256:a1", Labels: label("app")}}
	st.runner.onRun = func() {
		st.fake.Containers = []container.Summary{{ID: "a2", Image: "app:v1", ImageID: "sha256:a2", Labels: label("app")}}
	}
	if c, body := st.do(t, "POST", "local", "", `{"name":"web","compose":"services: {}\n"}`); c != http.StatusCreated {
		t.Fatalf("create stack = %d %s", c, body)
	}
	hook := createHook(t, st.h, st.cookie, hooksPath(st.envTest, "local"), `{"name":"ci","stack":"web"}`)
	if hook.Kind != "stack" || hook.Target != "web" {
		t.Errorf("hook = %+v", hook)
	}
	rec := trigger(t, st.h, hook.ID, "?wait=1", "", bearer(hook.Secret))
	run := runOf(t, rec)
	if rec.Code != http.StatusOK || run.Status != "updated" || run.ImageBefore != "app=sha256:a1" || run.ImageAfter != "app=sha256:a2" {
		t.Errorf("trigger = %d %s", rec.Code, rec.Body)
	}
	if st.runner.calls[len(st.runner.calls)-1] != "up web pull=true" {
		t.Errorf("runner calls = %q", st.runner.calls)
	}
	if hist := history(t, st.envTest, "local", "?kind=stack&target=web"); len(hist) != 1 || hist[0].User != "admin" {
		t.Errorf("history = %+v", hist)
	}

	// A failing compose fails the run, and the CI step with it.
	st.runner.code = 1
	rec = trigger(t, st.h, hook.ID, "?wait=1", "", bearer(hook.Secret))
	if run := runOf(t, rec); rec.Code != http.StatusBadGateway || run.Error != "compose exited with code 1" {
		t.Errorf("failing compose = %d %s", rec.Code, rec.Body)
	}
}

// failingBody breaks off after its first bytes, like a client that goes silent.
type failingBody struct{ sent bool }

func (b *failingBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, errors.New("read timeout")
	}
	b.sent = true
	return copy(p, "{"), nil
}

func TestTriggerDeployHookBody(t *testing.T) {
	t.Parallel()
	et, _ := newDeployHookTest(t, "sha256:img2")
	hook := createHook(t, et.h, et.cookie, hooksPath(et, "local"), `{"name":"ci","container":"web"}`)
	send := func(contentLength int64, body io.Reader, secret string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/"+hook.ID, body)
		r.ContentLength = contentLength
		r.Header.Set("Authorization", "Bearer "+secret)
		rec := httptest.NewRecorder()
		et.h.ServeHTTP(rec, r)
		return rec
	}
	// Refused by its length before it is read.
	rec := send(maxHookBody+1, strings.NewReader("{}"), hook.Secret)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "too large") {
		t.Errorf("long body = %d %s, want 413", rec.Code, rec.Body)
	}
	rec = send(-1, &failingBody{}, hook.Secret)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("broken body = %d %s, want a 400 JSON error", rec.Code, rec.Body)
	}

	// The IP's limit comes first: a blocked client's body isn't even read.
	for i := range 20 {
		if rec := trigger(t, et.h, hook.ID, "", "", bearer(hook.Secret+"x")); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("attempt %d = %d", i, rec.Code)
		}
	}
	if rec := send(maxHookBody+1, strings.NewReader("{}"), hook.Secret); rec.Code != http.StatusTooManyRequests {
		t.Errorf("long body from a blocked IP = %d, want 429", rec.Code)
	}
}

func TestDeployError(t *testing.T) {
	t.Parallel()
	daemon := errors.New("Error response from daemon: secret stuff")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "pinned", err: errPinnedImage, want: errPinnedImage.msg},
		{name: "busy container", err: errRecreating, want: errRecreating.msg},
		{name: "busy stack", err: errStackBusy, want: "the stack is being changed"},
		{name: "no stack", err: errNoStack, want: "stack not found"},
		{name: "pull", err: &pullFailure{err: daemon}, want: "pull failed"},
		{name: "refused create", err: refusal(daemon, "created", nil), want: "the container could not be replaced"},
		{name: "timeout", err: fmt.Errorf("pull: %w", context.DeadlineExceeded), want: "did not answer in time"},
		{name: "cancelled", err: context.Canceled, want: "cancelled"},
		{name: "old agent", err: fmt.Errorf("sync stack: %w", tunnel.ErrNoControl), want: "update the agent"},
		{name: "other", err: daemon, want: "the update failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := deployError(tt.err)
			if !strings.Contains(got, tt.want) || strings.Contains(got, "secret stuff") {
				t.Errorf("deployError = %q, want it to say %q", got, tt.want)
			}
		})
	}
}

func TestAgentDeployHook(t *testing.T) {
	t.Parallel()
	at, _ := newAgentStackTest(t, 2)
	if c, body := at.stack(t, "POST", "", `{"name":"web","compose":"services: {}\n"}`); c != http.StatusCreated {
		t.Fatalf("create stack = %d %s", c, body)
	}
	hook := createHook(t, at.h, at.cookie, fmt.Sprintf("/api/v1/environments/%d/webhooks", at.envID), `{"name":"ci","stack":"web"}`)
	rec := trigger(t, at.h, hook.ID, "?wait=1", "", bearer(hook.Secret))
	// The fake agent's containers carry no compose labels: nothing changed.
	if run := runOf(t, rec); rec.Code != http.StatusOK || run.Status != "unchanged" {
		t.Errorf("trigger = %d %s", rec.Code, rec.Body)
	}
	// A container on the agent, by name.
	hook = createHook(t, at.h, at.cookie, fmt.Sprintf("/api/v1/environments/%d/webhooks", at.envID), `{"name":"web","container":"web"}`)
	if hook.Target != "web" {
		t.Errorf("hook = %+v", hook)
	}
	// fakeDockerAPI's web has no settings to recreate from: the run fails safely.
	rec = trigger(t, at.h, hook.ID, "?wait=1", "", bearer(hook.Secret))
	if run := runOf(t, rec); rec.Code != http.StatusBadGateway || run.Status != "failed" || run.Error == "" {
		t.Errorf("trigger = %d %s", rec.Code, rec.Body)
	}
}
