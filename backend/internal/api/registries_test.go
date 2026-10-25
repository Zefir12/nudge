package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRegistries(t *testing.T) {
	t.Parallel()
	h, svc := newTestHandler(t)
	if _, err := svc.CreateFirstUser(t.Context(), "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookieOf(t, do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds}))
	call := func(method, path, body string) (int, string) {
		t.Helper()
		rec := do(t, h, request{method: method, path: path, body: body, cookie: cookie})
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}

	steps := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode int
		// wantBody is the exact body, or with a leading "~" a part of it.
		wantBody string
	}{
		{name: "empty list", method: "GET", path: "/api/v1/registries", wantCode: 200, wantBody: "[]"},
		{name: "create", method: "POST", path: "/api/v1/registries", wantCode: 201,
			body:     `{"name":"GitHub","address":"https://ghcr.io/","username":"bot","password":"hunter2"}`,
			wantBody: `{"id":1,"name":"GitHub","address":"ghcr.io","username":"bot","kind":"generic","owners":[],"plain_http":false,"created_at":"2023-11-14T22:13:20Z","updated_at":"2023-11-14T22:13:20Z"}`},
		{name: "create forgejo", method: "POST", path: "/api/v1/registries", wantCode: 201,
			body:     `{"name":"Forge","address":"git.example.com","username":"bot","password":"tok","kind":"forgejo","owners":["zefir","team-a"],"plain_http":true}`,
			wantBody: `~"kind":"forgejo","owners":["zefir","team-a"],"plain_http":true`},
		{name: "create bad kind", method: "POST", path: "/api/v1/registries", wantCode: 400,
			body: `{"name":"X","address":"x.io","username":"bot","password":"x","kind":"harbor"}`, wantBody: `{"error":"kind must be generic or forgejo"}`},
		{name: "create owners on generic", method: "POST", path: "/api/v1/registries", wantCode: 400,
			body: `{"name":"X","address":"x.io","username":"bot","password":"x","owners":["a"]}`, wantBody: `{"error":"owners only apply to forgejo registries"}`},
		{name: "create bad owner", method: "POST", path: "/api/v1/registries", wantCode: 400,
			body: `{"name":"X","address":"x.io","username":"bot","password":"x","kind":"forgejo","owners":["../a"]}`, wantBody: "~owners must be"},
		{name: "update forgejo to generic", method: "PUT", path: "/api/v1/registries/2", wantCode: 200,
			body: `{"name":"Forge","address":"git.example.com","username":"bot"}`, wantBody: `~"kind":"generic","owners":[],"plain_http":false`},
		{name: "delete forgejo", method: "DELETE", path: "/api/v1/registries/2", wantCode: 204},
		{name: "create taken", method: "POST", path: "/api/v1/registries", wantCode: 409,
			body:     `{"name":"Other","address":"ghcr.io","username":"bot","password":"x"}`,
			wantBody: `{"error":"a registry with that name or address already exists"}`},
		{name: "create without password", method: "POST", path: "/api/v1/registries", wantCode: 400,
			body: `{"name":"Hub","address":"docker.io","username":"bot"}`, wantBody: `{"error":"password is required"}`},
		{name: "create bad address", method: "POST", path: "/api/v1/registries", wantCode: 400,
			body: `{"name":"Hub","address":"not a host","username":"bot","password":"x"}`, wantBody: "~address must be"},
		{name: "create malformed", method: "POST", path: "/api/v1/registries", body: `{`, wantCode: 400},
		{name: "get", method: "GET", path: "/api/v1/registries/1", wantCode: 200, wantBody: `~"username":"bot"`},
		{name: "get missing", method: "GET", path: "/api/v1/registries/2", wantCode: 404, wantBody: `{"error":"registry not found"}`},
		{name: "get bad id", method: "GET", path: "/api/v1/registries/abc", wantCode: 404},
		{name: "update keeps password", method: "PUT", path: "/api/v1/registries/1", wantCode: 200,
			body: `{"name":"GitHub","address":"ghcr.io","username":"bot2"}`, wantBody: `~"username":"bot2"`},
		{name: "update missing", method: "PUT", path: "/api/v1/registries/9", wantCode: 404,
			body: `{"name":"x","address":"x.io","username":"u"}`},
		{name: "update invalid", method: "PUT", path: "/api/v1/registries/1", wantCode: 400,
			body: `{"name":"","address":"ghcr.io","username":"u"}`},
		{name: "list", method: "GET", path: "/api/v1/registries", wantCode: 200, wantBody: `~"address":"ghcr.io"`},
		{name: "delete", method: "DELETE", path: "/api/v1/registries/1", wantCode: 204},
		{name: "delete again", method: "DELETE", path: "/api/v1/registries/1", wantCode: 404},
	}
	for _, st := range steps {
		code, body := call(st.method, st.path, st.body)
		if code != st.wantCode {
			t.Fatalf("%s: code = %d, want %d (%s)", st.name, code, st.wantCode, body)
		}
		if strings.Contains(body, "hunter2") || strings.Contains(body, "password\":") {
			t.Errorf("%s: body leaks the password: %s", st.name, body)
		}
		if part, ok := strings.CutPrefix(st.wantBody, "~"); ok {
			if !strings.Contains(body, part) {
				t.Errorf("%s: body = %s, want it to contain %s", st.name, body, part)
			}
		} else if st.wantBody != "" && body != st.wantBody {
			t.Errorf("%s: body = %s, want %s", st.name, body, st.wantBody)
		}
		if code >= 400 {
			var e struct{ Error string }
			if err := json.Unmarshal([]byte(body), &e); err != nil || e.Error == "" {
				t.Errorf("%s: body = %s, want a JSON error", st.name, body)
			}
		}
	}
}

func TestRegistriesNeedSession(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t)
	for _, req := range []request{
		{method: "GET", path: "/api/v1/registries"},
		{method: "POST", path: "/api/v1/registries", body: `{}`},
		{method: "GET", path: "/api/v1/registries/1"},
		{method: "PUT", path: "/api/v1/registries/1", body: `{}`},
		{method: "DELETE", path: "/api/v1/registries/1"},
	} {
		if rec := do(t, h, req); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without session: code = %d, want 401", req.method, req.path, rec.Code)
		}
	}
}
