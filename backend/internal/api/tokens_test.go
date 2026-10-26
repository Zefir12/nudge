package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// loggedIn creates the admin and returns the handler and a session cookie.
func loggedIn(t *testing.T) (http.Handler, *http.Cookie) {
	t.Helper()
	h, svc := newTestHandler(t)
	if _, err := svc.CreateFirstUser(context.Background(), "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	return h, sessionCookieOf(t, rec)
}

func createToken(t *testing.T, h http.Handler, cookie *http.Cookie, name string) createdTokenResponse {
	t.Helper()
	rec := do(t, h, request{method: "POST", path: "/api/v1/tokens", body: fmt.Sprintf(`{"name":%q}`, name), cookie: cookie})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create token: %d %s", rec.Code, rec.Body)
	}
	var resp createdTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestTokenEndpoints(t *testing.T) {
	t.Parallel()
	h, cookie := loggedIn(t)
	created := createToken(t, h, cookie, "ci")
	if !strings.HasPrefix(created.Token, "nudge_") || created.Prefix != created.Token[:10] || created.Name != "ci" {
		t.Errorf("created = %+v", created)
	}

	tests := []struct {
		name     string
		req      request
		wantCode int
		wantBody string
	}{
		{name: "empty name", req: request{method: "POST", path: "/api/v1/tokens", body: `{"name":""}`, cookie: cookie}, wantCode: 400},
		{name: "duplicate name", req: request{method: "POST", path: "/api/v1/tokens", body: `{"name":"ci"}`, cookie: cookie}, wantCode: 409},
		{name: "list needs auth", req: request{method: "GET", path: "/api/v1/tokens"}, wantCode: 401},
		{name: "create needs auth", req: request{method: "POST", path: "/api/v1/tokens", body: `{"name":"x"}`}, wantCode: 401},
		{name: "delete unknown", req: request{method: "DELETE", path: "/api/v1/tokens/999", cookie: cookie}, wantCode: 404},
		{name: "delete bad id", req: request{method: "DELETE", path: "/api/v1/tokens/abc", cookie: cookie}, wantCode: 404},
		{name: "list never shows the token", req: request{method: "GET", path: "/api/v1/tokens", cookie: cookie}, wantCode: 200, wantBody: `"name":"ci"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, tt.req)
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %s, want it to contain %s", rec.Body, tt.wantBody)
			}
			if strings.Contains(rec.Body.String(), created.Token) {
				t.Errorf("body leaks the token: %s", rec.Body)
			}
		})
	}

	// The token works on its own, and its use shows up in the list.
	rec := do(t, h, request{method: "GET", path: "/api/v1/tokens", headers: bearer(created.Token)})
	var list []tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 {
		t.Fatalf("list with bearer: %d %s", rec.Code, rec.Body)
	}
	if len(list) != 1 || list[0].ID != created.ID || list[0].LastUsedAt == nil {
		t.Errorf("list = %+v", list)
	}

	rec = do(t, h, request{method: "DELETE", path: fmt.Sprintf("/api/v1/tokens/%d", created.ID), cookie: cookie})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, request{method: "GET", path: "/api/v1/auth/me", headers: bearer(created.Token)})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token: code = %d, want 401", rec.Code)
	}
}

func TestBearerAuth(t *testing.T) {
	t.Parallel()
	h, cookie := loggedIn(t)
	token := createToken(t, h, cookie, "ci").Token
	tests := []struct {
		name     string
		req      request
		wantCode int
	}{
		{name: "bearer", req: request{method: "GET", path: "/api/v1/auth/me", headers: bearer(token)}, wantCode: 200},
		{name: "lowercase scheme", req: request{method: "GET", path: "/api/v1/auth/me", headers: map[string]string{"Authorization": "bearer " + token}}, wantCode: 200},
		{name: "unknown token", req: request{method: "GET", path: "/api/v1/auth/me", headers: bearer("nudge_nope")}, wantCode: 401},
		{name: "wrong scheme", req: request{method: "GET", path: "/api/v1/auth/me", headers: map[string]string{"Authorization": "Basic " + token}}, wantCode: 401},
		{name: "empty bearer", req: request{method: "GET", path: "/api/v1/auth/me", headers: map[string]string{"Authorization": "Bearer "}}, wantCode: 401},
		// A session token is not an API token.
		{name: "session value as bearer", req: request{method: "GET", path: "/api/v1/auth/me", headers: bearer(cookie.Value)}, wantCode: 401},
		// The Authorization header decides; a valid cookie doesn't rescue a bad token.
		{name: "bad bearer with cookie", req: request{method: "GET", path: "/api/v1/auth/me", cookie: cookie, headers: bearer("nudge_nope")}, wantCode: 401},
		// No Origin check for bearer requests.
		{name: "bearer cross-origin write", req: request{method: "DELETE", path: "/api/v1/tokens/999", headers: map[string]string{"Authorization": "Bearer " + token, "Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}}, wantCode: 404},
		{name: "cookie cross-origin write", req: request{method: "POST", path: "/api/v1/tokens", body: `{"name":"y"}`, cookie: cookie, headers: map[string]string{"Origin": "https://evil.example"}}, wantCode: 403},
		// Public routes ignore the header; browsers can't send it cross-origin anyway.
		{name: "public route with bearer", req: request{method: "GET", path: "/api/v1/health", headers: bearer("nudge_nope")}, wantCode: 200},
		{name: "login with bogus bearer", req: request{method: "POST", path: "/api/v1/auth/login", body: adminCreds, headers: bearer("nudge_nope")}, wantCode: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, tt.req)
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
		})
	}
}

// TestTokensNeedSession checks that an API token can't mint another token that would
// outlive its revocation (#33), while a session still can and the token can still list
// and revoke.
func TestTokensNeedSession(t *testing.T) {
	t.Parallel()
	h, cookie := loggedIn(t)
	token := createToken(t, h, cookie, "ci").Token
	other := createToken(t, h, cookie, "other")
	tests := []struct {
		name     string
		req      request
		wantCode int
		wantBody string
	}{
		{name: "create with token", req: request{method: "POST", path: "/api/v1/tokens", body: `{"name":"minted"}`, headers: bearer(token)}, wantCode: 403, wantBody: `{"error":"API tokens can't do this; sign in to the web UI"}`},
		// The body isn't read: a malformed one is refused the same way.
		{name: "create with token, bad body", req: request{method: "POST", path: "/api/v1/tokens", body: `{`, headers: bearer(token)}, wantCode: 403},
		// A cookie next to the token doesn't help: the Authorization header decides.
		{name: "create with token and cookie", req: request{method: "POST", path: "/api/v1/tokens", body: `{"name":"minted"}`, cookie: cookie, headers: bearer(token)}, wantCode: 403},
		{name: "create with unknown token", req: request{method: "POST", path: "/api/v1/tokens", body: `{"name":"minted"}`, headers: bearer("nudge_nope")}, wantCode: 401},
		{name: "list with token", req: request{method: "GET", path: "/api/v1/tokens", headers: bearer(token)}, wantCode: 200},
		{name: "revoke with token", req: request{method: "DELETE", path: fmt.Sprintf("/api/v1/tokens/%d", other.ID), headers: bearer(token)}, wantCode: 204},
		{name: "create with session", req: request{method: "POST", path: "/api/v1/tokens", body: `{"name":"fine"}`, cookie: cookie}, wantCode: 201},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, tt.req)
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
			if got := strings.TrimSpace(rec.Body.String()); tt.wantBody != "" && got != tt.wantBody {
				t.Errorf("body = %s, want %s", got, tt.wantBody)
			}
		})
	}

	// Nothing was minted with the token, and each refusal is in the audit log under its name.
	rec := do(t, h, request{method: "GET", path: "/api/v1/tokens", cookie: cookie})
	if strings.Contains(rec.Body.String(), "minted") {
		t.Errorf("a token minted another: %s", rec.Body)
	}
	refused := 0
	for _, e := range keys(auditEntries(t, h, cookie, "").Entries) {
		if e == (entryKey{User: "admin", Token: "ci", Action: "POST /api/v1/tokens", Status: 403}) {
			refused++
		}
	}
	if refused != 3 {
		t.Errorf("audit has %d refused creates by token ci, want 3", refused)
	}
}

// TestSessionOnlyRoutesRefuseTokens catches a pattern that is public too, or that names
// no route (a typo would leave the real route open to tokens).
func TestSessionOnlyRoutesRefuseTokens(t *testing.T) {
	t.Parallel()
	h, cookie := loggedIn(t)
	token := createToken(t, h, cookie, "ci").Token
	wildcard := regexp.MustCompile(`\{[^}]+\}`)
	for pattern := range sessionOnlyRoutes {
		t.Run(pattern, func(t *testing.T) {
			if publicRoutes[pattern] {
				t.Errorf("%s is both public and session-only", pattern)
			}
			method, path, _ := strings.Cut(wildcard.ReplaceAllString(pattern, "1"), " ")
			rec := do(t, h, request{method: method, path: path, body: `{}`, headers: bearer(token)})
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s with a token: code = %d, want 403 (%s)", method, path, rec.Code, rec.Body)
			}
		})
	}
}
