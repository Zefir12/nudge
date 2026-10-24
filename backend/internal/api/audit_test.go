package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/moby/moby/api/types/jsonstream"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func auditEntries(t *testing.T, h http.Handler, cookie *http.Cookie, query string) auditListResponse {
	t.Helper()
	rec := do(t, h, request{method: "GET", path: "/api/v1/audit" + query, cookie: cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET audit%s: %d %s", query, rec.Code, rec.Body)
	}
	var resp auditListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// entryKey leaves out the ID and time, which the test doesn't control.
type entryKey struct {
	User, Token, Environment, Action, Target string
	Status                                   int
}

func keys(entries []auditEntryResponse) []entryKey {
	out := make([]entryKey, 0, len(entries))
	for _, e := range entries {
		out = append(out, entryKey{e.User, e.Token, e.Environment, e.Action, e.Target, e.Status})
	}
	return out
}

func TestAuditRecordsWrites(t *testing.T) {
	t.Parallel()
	et := newResourceTest(t)
	env := fmt.Sprint(et.ids["local"])
	base := "/api/v1/environments/" + env
	token := createToken(t, et.h, et.cookie, "ci")
	steps := []struct {
		req  request
		code int
	}{
		{request{method: "POST", path: base + "/containers/web/start", cookie: et.cookie}, 204},
		{request{method: "POST", path: base + "/volumes", body: `{"name":"cache"}`, cookie: et.cookie}, 201},
		{request{method: "DELETE", path: base + "/containers/nope", cookie: et.cookie}, 404},
		{request{method: "POST", path: base + "/containers/web/stop", headers: bearer(token.Token)}, 204},
		// Not recorded: reads, logins, unknown routes, unauthenticated requests.
		{request{method: "GET", path: base + "/containers", cookie: et.cookie}, 200},
		{request{method: "POST", path: "/api/v1/auth/login", body: adminCreds}, 200},
		{request{method: "POST", path: "/api/v1/nope", cookie: et.cookie}, 404},
		{request{method: "POST", path: base + "/containers/web/kill"}, 401},
		{request{method: "POST", path: "/api/v1/setup", body: adminCreds}, 409},
	}
	for _, s := range steps {
		if rec := do(t, et.h, s.req); rec.Code != s.code {
			t.Fatalf("%s %s: %d %s, want %d", s.req.method, s.req.path, rec.Code, rec.Body, s.code)
		}
	}

	got := auditEntries(t, et.h, et.cookie, "")
	want := []entryKey{
		{"admin", "ci", env, "POST /api/v1/environments/{env}/containers/{id}/stop", "web", 204},
		{"admin", "", env, "DELETE /api/v1/environments/{env}/containers/{id}", "nope", 404},
		{"admin", "", env, "POST /api/v1/environments/{env}/volumes", "cache", 201},
		{"admin", "", env, "POST /api/v1/environments/{env}/containers/{id}/start", "web", 204},
		{"admin", "", "", "POST /api/v1/tokens", "ci", 201},
	}
	if g := keys(got.Entries); fmt.Sprint(g) != fmt.Sprint(want) {
		t.Errorf("entries:\n got %v\nwant %v", g, want)
	}
	if got.Next != nil {
		t.Errorf("next = %d, want nil", *got.Next)
	}
	if e := got.Entries[0]; e.Time.IsZero() || e.ID == 0 {
		t.Errorf("entry without time or id: %+v", e)
	}

	if exact := auditEntries(t, et.h, et.cookie, "?limit=5"); len(exact.Entries) != 5 || exact.Next != nil {
		t.Errorf("a page holding the rest: %d entries, next %v; want 5 and nil", len(exact.Entries), exact.Next)
	}
	page := auditEntries(t, et.h, et.cookie, "?limit=2")
	if len(page.Entries) != 2 || page.Next == nil {
		t.Fatalf("first page = %+v", page)
	}
	page = auditEntries(t, et.h, et.cookie, fmt.Sprintf("?limit=2&before=%d", *page.Next))
	if g := keys(page.Entries); fmt.Sprint(g) != fmt.Sprint(want[2:4]) {
		t.Errorf("second page = %v, want %v", g, want[2:4])
	}

	filters := []struct {
		query string
		want  []entryKey
	}{
		{"?failed=true", want[1:2]},
		{"?q=volumes", want[2:3]},
		{"?user=nobody", nil},
		{"?environment=" + env + "&failed=false", []entryKey{want[0], want[2], want[3]}},
		{"?since=2000-01-01T00:00:00Z&until=2001-01-01T00:00:00Z", nil},
	}
	for _, f := range filters {
		if g := keys(auditEntries(t, et.h, et.cookie, f.query).Entries); fmt.Sprint(g) != fmt.Sprint(f.want) {
			t.Errorf("%s: got %v, want %v", f.query, g, f.want)
		}
	}
}

func TestAuditListErrors(t *testing.T) {
	t.Parallel()
	h, cookie := loggedIn(t)
	for _, q := range []string{"?limit=201", "?limit=-1", "?limit=x", "?before=x", "?failed=maybe", "?since=yesterday", "?until=1"} {
		rec := do(t, h, request{method: "GET", path: "/api/v1/audit" + q, cookie: cookie})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", q, rec.Code, rec.Body)
		}
	}
	if rec := do(t, h, request{method: "GET", path: "/api/v1/audit"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a session: %d, want 401", rec.Code)
	}
}

func TestAuditRecordsSetup(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t)
	rec := do(t, h, request{method: "POST", path: "/api/v1/setup", body: adminCreds})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	got := auditEntries(t, h, sessionCookieOf(t, rec), "")
	want := []entryKey{{"", "", "", "POST /api/v1/setup", "admin", 201}}
	if g := keys(got.Entries); fmt.Sprint(g) != fmt.Sprint(want) {
		t.Errorf("entries = %v, want %v", g, want)
	}
}

func TestAuditPullStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msgs []jsonstream.Message
		want int
	}{
		{"done", []jsonstream.Message{{Status: "Pulling"}}, 200},
		{"fails after it started", []jsonstream.Message{{Status: "Pulling"}, {Error: &jsonstream.Error{Message: "denied"}}}, 502},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			et := newEnvTest(t, map[string]*dockertest.Fake{"local": {Resources: dockertest.Resources{PullMessages: tt.msgs}}})
			path := fmt.Sprintf("/api/v1/environments/%d/images/pull", et.ids["local"])
			if rec := do(t, et.h, request{method: "POST", path: path, cookie: et.cookie, body: `{"image":" nginx:1.29 "}`}); rec.Code != 200 {
				t.Fatalf("pull: %d %s", rec.Code, rec.Body)
			}
			got := auditEntries(t, et.h, et.cookie, "")
			if len(got.Entries) != 1 || got.Entries[0].Status != tt.want || got.Entries[0].Target != "nginx:1.29" {
				t.Errorf("entries = %+v, want one for nginx:1.29 with status %d", got.Entries, tt.want)
			}
		})
	}
}
