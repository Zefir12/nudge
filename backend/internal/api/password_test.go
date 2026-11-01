package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
)

func TestChangePassword(t *testing.T) {
	t.Parallel()
	h, _, admin := usersTest(t)
	bob := addUser(t, h, admin, "bob", auth.RoleUser)
	bobOther := sessionCookieOf(t, do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: `{"username":"bob","password":"longenough"}`}))
	rec := do(t, h, request{method: "POST", path: "/api/v1/tokens", cookie: bob, body: `{"name":"ci"}`})
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || tok.Token == "" {
		t.Fatalf("token: %s", rec.Body)
	}

	tests := []struct {
		name     string
		req      request
		wantCode int
	}{
		{"no session", request{body: `{"current":"longenough","new":"evenlonger"}`}, http.StatusUnauthorized},
		{"API token", request{headers: map[string]string{"Authorization": "Bearer " + tok.Token}, body: `{"current":"longenough","new":"evenlonger"}`}, http.StatusForbidden},
		{"wrong current", request{cookie: bob, body: `{"current":"not-the-one","new":"evenlonger"}`}, http.StatusForbidden},
		{"short new", request{cookie: bob, body: `{"current":"longenough","new":"short"}`}, http.StatusBadRequest},
	}
	for _, tt := range tests {
		tt.req.method, tt.req.path = "POST", "/api/v1/auth/password"
		if rec := do(t, h, tt.req); rec.Code != tt.wantCode {
			t.Errorf("%s: code = %d %s, want %d", tt.name, rec.Code, rec.Body, tt.wantCode)
		}
	}

	rec = do(t, h, request{method: "POST", path: "/api/v1/auth/password", cookie: bob, body: `{"current":"longenough","new":"evenlonger"}`})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("change = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, request{method: "GET", path: "/api/v1/auth/me", cookie: bob}); rec.Code != http.StatusOK {
		t.Errorf("own session after the change = %d", rec.Code)
	}
	if rec := do(t, h, request{method: "GET", path: "/api/v1/auth/me", cookie: bobOther}); rec.Code != http.StatusUnauthorized {
		t.Errorf("other session after the change = %d", rec.Code)
	}
	if rec := do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: `{"username":"bob","password":"evenlonger"}`}); rec.Code != http.StatusOK {
		t.Errorf("login with the new password = %d", rec.Code)
	}

	rec = do(t, h, request{method: "GET", path: "/api/v1/audit", cookie: admin})
	if !strings.Contains(rec.Body.String(), "POST /api/v1/auth/password") {
		t.Errorf("change not audited: %s", rec.Body)
	}
	for _, secret := range []string{"evenlonger", "not-the-one"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("audit log holds a password: %s", rec.Body)
		}
	}
}
