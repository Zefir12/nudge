package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
)

// addUser creates a local user through the API as the admin of adminCookie and returns
// the new user's session cookie.
func addUser(t *testing.T, h http.Handler, adminCookie *http.Cookie, name string, role auth.Role) *http.Cookie {
	t.Helper()
	body := fmt.Sprintf(`{"username":%q,"password":"longenough","role":%q}`, name, role)
	if rec := do(t, h, request{method: "POST", path: "/api/v1/users", cookie: adminCookie, body: body}); rec.Code != http.StatusCreated {
		t.Fatalf("create user %s = %d %s", name, rec.Code, rec.Body)
	}
	rec := do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: fmt.Sprintf(`{"username":%q,"password":"longenough"}`, name)})
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s = %d %s", name, rec.Code, rec.Body)
	}
	return sessionCookieOf(t, rec)
}

func usersTest(t *testing.T) (http.Handler, *auth.Service, *http.Cookie) {
	t.Helper()
	h, svc := newTestHandler(t)
	if _, err := svc.CreateFirstUser(context.Background(), "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	return h, svc, sessionCookieOf(t, do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds}))
}

func accountOf(t *testing.T, body string) accountResponse {
	t.Helper()
	var a accountResponse
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatalf("account: %v (%s)", err, body)
	}
	return a
}

var pathParam = regexp.MustCompile(`\{[a-z]+\}`)

// TestRoutesForUsers walks every registered route: a user's session and API token get
// 403 on each one outside userRoutes, so a new route is admin-only until it is opened.
func TestRoutesForUsers(t *testing.T) {
	t.Parallel()
	h, svc, admin := usersTest(t)
	user := addUser(t, h, admin, "bob", auth.RoleUser)
	var bobID int64
	for _, a := range listUsers(t, h, admin) {
		if a.Username == "bob" {
			bobID = a.ID
		}
	}
	_, token, err := svc.CreateToken(context.Background(), bobID, "ci")
	if err != nil {
		t.Fatal(err)
	}

	registered := map[string]bool{"/api/": true}
	for _, rt := range routes {
		registered[rt.pattern] = true
	}
	for name, set := range map[string]map[string]bool{"userRoutes": userRoutes, "publicRoutes": publicRoutes, "sessionOnlyRoutes": sessionOnlyRoutes} {
		for p := range set {
			if !registered[p] {
				t.Errorf("%s has %q, which isn't a route", name, p)
			}
		}
	}

	for p := range registered {
		if publicRoutes[p] {
			continue
		}
		method, path, ok := strings.Cut(p, " ")
		if !ok {
			method, path = "GET", "/api/v1/nope"
		}
		// Placeholders name nothing that exists, so open routes change nothing.
		path = pathParam.ReplaceAllString(path, "999999")
		for _, cred := range []struct {
			name string
			req  request
		}{
			{"session", request{cookie: user}},
			{"token", request{headers: bearer(token)}},
		} {
			req := cred.req
			req.method, req.path, req.body = method, path, "{}"
			rec := do(t, h, req)
			adminsOnly := rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "admins only")
			// Session-only routes refuse every token before the role is checked.
			tokenRefused := cred.name == "token" && sessionOnlyRoutes[p] && rec.Code == http.StatusForbidden
			if userRoutes[p] && adminsOnly {
				t.Errorf("%s with a user %s = %d %s, want it open to users", p, cred.name, rec.Code, rec.Body)
			}
			refused := adminsOnly || tokenRefused
			if !userRoutes[p] && !refused {
				t.Errorf("%s with a user %s = %d %s, want 403 admins only", p, cred.name, rec.Code, rec.Body)
			}
		}
	}

	// The refusal of a write is audited with the user.
	do(t, h, request{method: "POST", path: "/api/v1/registries", cookie: user, body: "{}"})
	entries := auditEntries(t, h, admin, "?limit=1").Entries
	if len(entries) != 1 || entries[0].User != "bob" || entries[0].Action != "POST /api/v1/registries" || entries[0].Status != http.StatusForbidden {
		t.Errorf("audit = %+v", entries)
	}
}

func listUsers(t *testing.T, h http.Handler, cookie *http.Cookie) []accountResponse {
	t.Helper()
	rec := do(t, h, request{method: "GET", path: "/api/v1/users", cookie: cookie})
	var out []accountResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("list users = %d %s", rec.Code, rec.Body)
	}
	return out
}

func TestMeHasRole(t *testing.T) {
	t.Parallel()
	h, _, admin := usersTest(t)
	user := addUser(t, h, admin, "bob", auth.RoleUser)
	for _, tt := range []struct {
		cookie *http.Cookie
		want   string
	}{
		{admin, `"role":"admin","admin":true`},
		{user, `"role":"user","admin":false`},
	} {
		rec := do(t, h, request{method: "GET", path: "/api/v1/auth/me", cookie: tt.cookie})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("me = %d %s, want %s", rec.Code, rec.Body, tt.want)
		}
	}
}

func TestUserManagement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, svc, admin := usersTest(t)
	user := addUser(t, h, admin, "bob", auth.RoleUser)
	users := listUsers(t, h, admin)
	if len(users) != 2 || users[0].Username != "admin" || users[0].Role != auth.RoleAdmin || users[1].Username != "bob" || users[1].Role != auth.RoleUser || users[1].SSO {
		t.Fatalf("users = %+v", users)
	}
	adminID, bobID := users[0].ID, users[1].ID
	_, bobToken, err := svc.CreateToken(ctx, bobID, "ci")
	if err != nil {
		t.Fatal(err)
	}
	me := func(req request) int {
		t.Helper()
		req.method, req.path = "GET", "/api/v1/auth/me"
		return do(t, h, req).Code
	}
	patch := func(id int64, body string) (int, string) {
		t.Helper()
		rec := do(t, h, request{method: "PATCH", path: fmt.Sprintf("/api/v1/users/%d", id), cookie: admin, body: body})
		return rec.Code, rec.Body.String()
	}

	tests := []struct {
		name string
		path string
		body string
		code int
		want string
	}{
		{name: "create duplicate", body: `{"username":"BOB","password":"longenough","role":"user"}`, code: 409, want: "already exists"},
		{name: "create bad role", body: `{"username":"carol","password":"longenough","role":"root"}`, code: 400, want: "role must be"},
		{name: "create short password", body: `{"username":"carol","password":"short","role":"user"}`, code: 400, want: "password must be"},
		{name: "create no name", body: `{"username":"","password":"longenough","role":"user"}`, code: 400, want: "username must be"},
	}
	for _, tt := range tests {
		rec := do(t, h, request{method: "POST", path: "/api/v1/users", cookie: admin, body: tt.body})
		if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("%s = %d %s, want %d %s", tt.name, rec.Code, rec.Body, tt.code, tt.want)
		}
	}
	for _, tt := range []struct {
		name string
		id   int64
		body string
		code int
		want string
	}{
		{"unknown user", 999, `{"disabled":true}`, 404, "user not found"},
		{"bad role", bobID, `{"role":"root"}`, 400, "role must be"},
		{"short password", bobID, `{"password":"short"}`, 400, "password must be"},
		{"demote the last admin", adminID, `{"role":"user"}`, 409, "last enabled admin"},
		{"disable the last admin", adminID, `{"disabled":true}`, 409, "last enabled admin"},
	} {
		if code, body := patch(tt.id, tt.body); code != tt.code || !strings.Contains(body, tt.want) {
			t.Errorf("%s = %d %s, want %d %s", tt.name, code, body, tt.code, tt.want)
		}
	}
	if rec := do(t, h, request{method: "DELETE", path: fmt.Sprintf("/api/v1/users/%d", adminID), cookie: admin}); rec.Code != http.StatusConflict {
		t.Errorf("delete the last admin = %d %s", rec.Code, rec.Body)
	}

	// Disabling ends bob's sessions and stops his token; enabling brings the token back.
	if code, body := patch(bobID, `{"disabled":true}`); code != 200 || !accountOf(t, body).Disabled {
		t.Fatalf("disable = %d %s", code, body)
	}
	if c := me(request{cookie: user}); c != http.StatusUnauthorized {
		t.Errorf("disabled user's session = %d", c)
	}
	if c := me(request{headers: bearer(bobToken)}); c != http.StatusUnauthorized {
		t.Errorf("disabled user's token = %d", c)
	}
	if rec := do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: `{"username":"bob","password":"longenough"}`}); rec.Code != http.StatusUnauthorized {
		t.Errorf("disabled user's login = %d", rec.Code)
	}
	if code, body := patch(bobID, `{"disabled":false}`); code != 200 || accountOf(t, body).Disabled {
		t.Fatalf("enable = %d %s", code, body)
	}
	if c := me(request{headers: bearer(bobToken)}); c != http.StatusOK {
		t.Errorf("enabled user's token = %d", c)
	}

	// Promoted, bob is an admin, and then the admin can be demoted.
	if code, body := patch(bobID, `{"role":"admin"}`); code != 200 || accountOf(t, body).Role != auth.RoleAdmin {
		t.Fatalf("promote = %d %s", code, body)
	}
	if code, body := patch(adminID, `{"role":"user"}`); code != 200 {
		t.Fatalf("demote admin = %d %s", code, body)
	}
	if code, body := patch(adminID, `{"role":"admin"}`); code != http.StatusForbidden {
		t.Errorf("demoted admin's own session still changes users: %d %s", code, body)
	}

	// A password reset ends the sessions; the new password logs in.
	bobAdmin := sessionCookieOf(t, do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: `{"username":"bob","password":"longenough"}`}))
	rec := do(t, h, request{method: "PATCH", path: fmt.Sprintf("/api/v1/users/%d", adminID), cookie: bobAdmin, body: `{"password":"evenlonger","role":"admin"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d %s", rec.Code, rec.Body)
	}
	if c := me(request{cookie: admin}); c != http.StatusUnauthorized {
		t.Errorf("session after a password reset = %d", c)
	}
	if rec := do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: `{"username":"admin","password":"evenlonger"}`}); rec.Code != http.StatusOK {
		t.Errorf("login with the new password = %d", rec.Code)
	}

	// An SSO user's role is the provider's; they can only be disabled.
	if _, err := svc.SSOLogin(ctx, auth.Identity{Issuer: "https://idp.example.net", Subject: "s-1", Username: "carol", Role: auth.RoleUser}); err != nil {
		t.Fatal(err)
	}
	var carol accountResponse
	for _, a := range listUsers(t, h, bobAdmin) {
		if a.Username == "carol" {
			carol = a
		}
	}
	if !carol.SSO || carol.Role != auth.RoleUser {
		t.Fatalf("carol = %+v", carol)
	}
	for _, body := range []string{`{"role":"admin"}`, `{"password":"longenough"}`} {
		rec := do(t, h, request{method: "PATCH", path: fmt.Sprintf("/api/v1/users/%d", carol.ID), cookie: bobAdmin, body: body})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("SSO user %s = %d %s", body, rec.Code, rec.Body)
		}
	}

	// Deleting ends sessions and tokens; it is audited with the username.
	rec = do(t, h, request{method: "DELETE", path: fmt.Sprintf("/api/v1/users/%d", adminID), cookie: bobAdmin})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	entries := auditEntries(t, h, bobAdmin, "?limit=1").Entries
	if len(entries) != 1 || entries[0].Action != "DELETE /api/v1/users/{id}" || entries[0].Target != "admin" || entries[0].User != "bob" {
		t.Errorf("audit = %+v", entries)
	}
	if rec := do(t, h, request{method: "DELETE", path: fmt.Sprintf("/api/v1/users/%d", bobID), cookie: bobAdmin}); rec.Code != http.StatusConflict {
		t.Errorf("delete the last admin = %d %s", rec.Code, rec.Body)
	}
}

func TestUserManagementNeedsASession(t *testing.T) {
	t.Parallel()
	h, svc, _ := usersTest(t)
	_, token, err := svc.CreateToken(context.Background(), 1, "ci")
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []request{
		{method: "POST", path: "/api/v1/users", body: `{"username":"carol","password":"longenough","role":"admin"}`},
		{method: "PATCH", path: "/api/v1/users/1", body: `{"password":"evenlonger"}`},
	} {
		req.headers = bearer(token)
		if rec := do(t, h, req); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "API tokens can't") {
			t.Errorf("%s %s with a token = %d %s", req.method, req.path, rec.Code, rec.Body)
		}
	}
	if rec := do(t, h, request{method: "GET", path: "/api/v1/users", headers: bearer(token)}); rec.Code != http.StatusOK {
		t.Errorf("list with an admin token = %d %s", rec.Code, rec.Body)
	}
}
