package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"

	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

// newDeploymentTest is a stack test with the stack web (app on app:v1, .env setting
// API_IMAGE and KEEP) and the deployment shop on it, which lets deploys set API_IMAGE.
func newDeploymentTest(t *testing.T, mods ...func(*Options)) stackTest {
	t.Helper()
	st := newStackTest(t, mods...)
	label := map[string]string{stacks.ProjectLabel: "web", stacks.ServiceLabel: "app"}
	st.fake.Containers = []container.Summary{{ID: "a1", Image: "app:v1", ImageID: "sha256:a1", Labels: label}}
	st.runner.onRun = func() {
		st.fake.Containers = []container.Summary{{ID: "a2", Image: "app:v2", ImageID: "sha256:a2", Labels: label}}
	}
	body := `{"name":"web","compose":"services:\n  app:\n    image: ${API_IMAGE}\n","env":"# pinned by CI\nAPI_IMAGE=app:v1\nKEEP=1\n"}`
	if c, out := st.do(t, "POST", "local", "", body); c != http.StatusCreated {
		t.Fatalf("create stack = %d %s", c, out)
	}
	rec := do(t, st.h, request{method: "POST", path: "/api/v1/deployments", cookie: st.cookie,
		body: fmt.Sprintf(`{"name":"shop","environment":%d,"stack":"web","variables":["API_IMAGE"]}`, st.ids["local"])})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create deployment = %d %s", rec.Code, rec.Body)
	}
	return st
}

// deployToken creates a deploy token of shop with scopes and returns its value.
func deployToken(t *testing.T, st stackTest, name string, scopes ...string) string {
	t.Helper()
	sc, _ := json.Marshal(scopes)
	rec := do(t, st.h, request{method: "POST", path: "/api/v1/deployments/shop/tokens", cookie: st.cookie,
		body: fmt.Sprintf(`{"name":%q,"scopes":%s}`, name, sc)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create deploy token = %d %s", rec.Code, rec.Body)
	}
	var out createDeployTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Token, "nudge_dt_") || out.Prefix != out.Token[:len(out.Prefix)] {
		t.Fatalf("token = %+v", out)
	}
	return out.Token
}

func deployRunOf(t *testing.T, body string) deploymentRunJSON {
	t.Helper()
	var run deploymentRunJSON
	if err := json.Unmarshal([]byte(body), &run); err != nil {
		t.Fatalf("run: %v (%s)", err, body)
	}
	return run
}

func TestDeploymentManagement(t *testing.T) {
	t.Parallel()
	st := newDeploymentTest(t)
	local := st.ids["local"]
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		code   int
		want   string
	}{
		{name: "get", method: "GET", path: "/api/v1/deployments/shop", code: 200, want: `"can":["view","deploy","edit","manage_tokens","manage_members","delete"]`},
		{name: "list", method: "GET", path: "/api/v1/deployments", code: 200, want: `"name":"shop"`},
		{name: "list by environment", method: "GET", path: fmt.Sprintf("/api/v1/deployments?environment=%d", local), code: 200, want: `"stack":"web"`},
		{name: "list bad environment", method: "GET", path: "/api/v1/deployments?environment=x", code: 400},
		{name: "unknown", method: "GET", path: "/api/v1/deployments/nope", code: 404},
		{name: "taken", method: "POST", path: "/api/v1/deployments", body: fmt.Sprintf(`{"name":"shop","environment":%d,"stack":"web"}`, local), code: 409},
		{name: "bad name", method: "POST", path: "/api/v1/deployments", body: fmt.Sprintf(`{"name":"Shop","environment":%d,"stack":"web"}`, local), code: 400, want: "lowercase"},
		{name: "unknown environment", method: "POST", path: "/api/v1/deployments", body: `{"name":"x","environment":999,"stack":"web"}`, code: 400, want: "environment not found"},
		{name: "unknown stack", method: "POST", path: "/api/v1/deployments", body: fmt.Sprintf(`{"name":"x","environment":%d,"stack":"nope"}`, local), code: 400, want: "no stack"},
		{name: "bad variable", method: "PATCH", path: "/api/v1/deployments/shop", body: `{"variables":["a b"]}`, code: 400},
		{name: "members", method: "GET", path: "/api/v1/deployments/shop/members", code: 200, want: `"username":"admin","role":"owner"`},
		{name: "unknown member", method: "POST", path: "/api/v1/deployments/shop/members", body: `{"username":"nobody","role":"viewer"}`, code: 400, want: "no user"},
		{name: "last owner", method: "POST", path: "/api/v1/deployments/shop/members", body: `{"username":"admin","role":"viewer"}`, code: 409},
		{name: "bad member id", method: "DELETE", path: "/api/v1/deployments/shop/members/x", code: 404},
		{name: "bad token scope", method: "POST", path: "/api/v1/deployments/shop/tokens", body: `{"name":"x","scopes":["admin"]}`, code: 400},
		{name: "no runs yet", method: "GET", path: "/api/v1/deployments/shop/deploys", code: 200, want: `[]`},
		{name: "unknown run", method: "GET", path: "/api/v1/deployments/shop/deploys/9", code: 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, st.h, request{method: tt.method, path: tt.path, body: tt.body, cookie: st.cookie})
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("%s %s = %d %s; want %d with %q", tt.method, tt.path, rec.Code, rec.Body, tt.code, tt.want)
			}
		})
	}

	rec := do(t, st.h, request{method: "PATCH", path: "/api/v1/deployments/shop", cookie: st.cookie, body: `{"description":"the shop"}`})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"description":"the shop","variables":["API_IMAGE"]`) {
		t.Errorf("change = %d %s", rec.Code, rec.Body)
	}

	// Deploy tokens are minted only with a session, never with an API token.
	apiToken := createToken(t, st.h, st.cookie, "script").Token
	rec = do(t, st.h, request{method: "POST", path: "/api/v1/deployments/shop/tokens", body: `{"name":"x","scopes":["deploy"]}`, headers: bearer(apiToken)})
	if rec.Code != http.StatusForbidden {
		t.Errorf("token with an API token = %d %s, want 403", rec.Code, rec.Body)
	}
	tok := deployToken(t, st, "ci", "deploy")
	rec = do(t, st.h, request{method: "GET", path: "/api/v1/deployments/shop/tokens", cookie: st.cookie})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), tok) || !strings.Contains(rec.Body.String(), `"scopes":["deploy"]`) {
		t.Errorf("token list = %d %s; want the token without its value", rec.Code, rec.Body)
	}
	var tokens []deployTokenJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &tokens); err != nil || len(tokens) != 1 {
		t.Fatalf("tokens = %s, %v", rec.Body, err)
	}
	if rec := do(t, st.h, request{method: "DELETE", path: "/api/v1/deployments/shop/tokens/" + strconv.FormatInt(tokens[0].ID, 10), cookie: st.cookie}); rec.Code != http.StatusNoContent {
		t.Errorf("revoke token = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, st.h, request{method: "POST", path: "/api/v1/deployments/shop/deploys", headers: bearer(tok)}); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token deploys = %d %s, want 401", rec.Code, rec.Body)
	}

	if rec := do(t, st.h, request{method: "DELETE", path: "/api/v1/deployments/shop", cookie: st.cookie}); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, st.h, request{method: "GET", path: "/api/v1/deployments/shop", cookie: st.cookie}); rec.Code != http.StatusNotFound {
		t.Errorf("after delete = %d, want 404", rec.Code)
	}
}

func TestDeployWithToken(t *testing.T) {
	t.Parallel()
	st := newDeploymentTest(t)
	tok := deployToken(t, st, "ci", "deploy", "runs:read")
	reader := deployToken(t, st, "status", "runs:read")

	rec := do(t, st.h, request{method: "POST", path: "/api/v1/deployments/shop/deploys?wait=1", body: `{"variables":{"API_IMAGE":"app:v2"}}`, headers: bearer(tok)})
	run := deployRunOf(t, rec.Body.String())
	if rec.Code != http.StatusOK || run.Status != "succeeded" || run.Actor != "ci" || run.ActorKind != "token" ||
		len(run.Previous) != 0 || run.ImageBefore != "app=sha256:a1" || run.ImageAfter != "app=sha256:a2" {
		t.Errorf("deploy = %d %s; want succeeded, without the replaced values", rec.Code, rec.Body)
	}
	// The owner sees what the run replaced.
	rec = do(t, st.h, request{method: "GET", path: "/api/v1/deployments/shop/deploys/" + strconv.FormatInt(run.ID, 10), cookie: st.cookie})
	if r := deployRunOf(t, rec.Body.String()); r.Previous["API_IMAGE"] != "app:v1" {
		t.Errorf("run for the owner = %s, want previous API_IMAGE app:v1", rec.Body)
	}
	env, err := os.ReadFile(filepath.Join(st.dir, strconv.FormatInt(st.ids["local"], 10), "web", ".env"))
	if err != nil || string(env) != "# pinned by CI\nAPI_IMAGE=app:v2\nKEEP=1\n" {
		t.Errorf(".env = %q, %v; want API_IMAGE replaced, the rest kept", env, err)
	}
	if got := st.runner.synced["web"].Env; !strings.Contains(got, "API_IMAGE=app:v2") {
		t.Errorf("synced .env = %q, want the new value", got)
	}
	// The update history names the token's creator.
	if hist := history(t, st.envTest, "local", "?kind=stack&target=web"); len(hist) != 1 || hist[0].User != "admin" {
		t.Errorf("history = %+v", hist)
	}

	// A failing compose fails the CI step and puts the .env back.
	st.runner.code = 1
	rec = do(t, st.h, request{method: "POST", path: "/api/v1/deployments/shop/deploys?wait=1", body: `{"variables":{"API_IMAGE":"app:broken"}}`, headers: bearer(tok)})
	if run := deployRunOf(t, rec.Body.String()); rec.Code != http.StatusBadGateway || run.Status != "failed" || run.Error != "pulling: compose exited with code 1" {
		t.Errorf("failing deploy = %d %s", rec.Code, rec.Body)
	}
	env, err = os.ReadFile(filepath.Join(st.dir, strconv.FormatInt(st.ids["local"], 10), "web", ".env"))
	if err != nil || string(env) != "# pinned by CI\nAPI_IMAGE=app:v2\nKEEP=1\n" {
		t.Errorf(".env after a failed run = %q, %v; want the last good one", env, err)
	}
	st.runner.code = 0

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		token  string
		code   int
	}{
		{name: "runs", method: "GET", path: "/api/v1/deployments/shop/deploys", token: reader, code: 200},
		{name: "one run", method: "GET", path: "/api/v1/deployments/shop/deploys/" + strconv.FormatInt(run.ID, 10), token: reader, code: 200},
		{name: "read-only token deploys", method: "POST", path: "/api/v1/deployments/shop/deploys", token: reader, code: 403},
		{name: "variable not allowed", method: "POST", path: "/api/v1/deployments/shop/deploys", body: `{"variables":{"KEEP":"2"}}`, token: tok, code: 400},
		{name: "value with interpolation", method: "POST", path: "/api/v1/deployments/shop/deploys", body: `{"variables":{"API_IMAGE":"${HOME}"}}`, token: tok, code: 400},
		{name: "other deployment", method: "POST", path: "/api/v1/deployments/other/deploys", token: tok, code: 401},
		{name: "other route", method: "GET", path: "/api/v1/environments", token: tok, code: 403},
		{name: "management route", method: "GET", path: "/api/v1/deployments/shop", token: tok, code: 403},
		{name: "mint a token", method: "POST", path: "/api/v1/deployments/shop/tokens", body: `{"name":"x","scopes":["deploy"]}`, token: tok, code: 403},
		{name: "unknown token", method: "POST", path: "/api/v1/deployments/shop/deploys", token: "nudge_dt_NOPE", code: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, st.h, request{method: tt.method, path: tt.path, body: tt.body, headers: bearer(tt.token)})
			if rec.Code != tt.code {
				t.Errorf("%s %s = %d %s; want %d", tt.method, tt.path, rec.Code, rec.Body, tt.code)
			}
		})
	}

	// A user deploy without waiting answers the queued run.
	rec = do(t, st.h, request{method: "POST", path: "/api/v1/deployments/shop/deploys", cookie: st.cookie})
	if run := deployRunOf(t, rec.Body.String()); rec.Code != http.StatusAccepted || run.ActorKind != "user" || run.Actor != "admin" {
		t.Errorf("user deploy = %d %s", rec.Code, rec.Body)
	}
}
