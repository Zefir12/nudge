package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type request struct {
	method  string
	path    string
	body    string
	cookie  *http.Cookie
	headers map[string]string
	// remote is the TCP peer; empty means httptest's 192.0.2.1:1234.
	remote string
}

func do(t *testing.T, h http.Handler, req request) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(req.method, req.path, strings.NewReader(req.body))
	if req.cookie != nil {
		r.AddCookie(req.cookie)
	}
	for k, v := range req.headers {
		r.Header.Set(k, v)
	}
	if req.remote != "" {
		r.RemoteAddr = req.remote
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func sessionCookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", sessionCookie, rec.Header().Values("Set-Cookie"))
	return nil
}

const adminCreds = `{"username":"admin","password":"longenough"}`

func TestSetupAndLogin(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t)

	tests := []struct {
		name     string
		body     string
		wantCode int
		wantErr  string
	}{
		{name: "malformed", body: `{`, wantCode: 400, wantErr: "invalid JSON body: malformed"},
		{name: "wrong type", body: `{"username":1}`, wantCode: 400, wantErr: "has the wrong type"},
		{name: "too large", body: `{"username":"` + strings.Repeat("a", maxBodyBytes) + `"}`, wantCode: 400, wantErr: "invalid JSON body: too large"},
		{name: "short password", body: `{"username":"admin","password":"short"}`, wantCode: 400, wantErr: "password must be"},
	}
	for _, tt := range tests {
		rec := do(t, h, request{method: "POST", path: "/api/v1/setup", body: tt.body})
		if rec.Code != tt.wantCode || !strings.Contains(rec.Body.String(), tt.wantErr) {
			t.Errorf("%s: %d %s, want %d %q", tt.name, rec.Code, rec.Body, tt.wantCode, tt.wantErr)
		}
	}

	rec := do(t, h, request{method: "POST", path: "/api/v1/setup", body: adminCreds})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	var u userResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil || u.Username != "admin" || u.ID == 0 {
		t.Errorf("setup body %s (%v)", rec.Body, err)
	}
	cookie := sessionCookieOf(t, rec)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure || cookie.Path != "/" {
		t.Errorf("cookie = %+v", cookie)
	}

	rec = do(t, h, request{method: "POST", path: "/api/v1/setup", body: `{"username":"evil","password":"longenough"}`})
	if rec.Code != http.StatusConflict {
		t.Errorf("second setup: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, request{method: "GET", path: "/api/v1/setup"})
	if got := strings.TrimSpace(rec.Body.String()); got != `{"needed":false}` {
		t.Errorf("setup status = %s", got)
	}

	rec = do(t, h, request{method: "GET", path: "/api/v1/auth/me", cookie: cookie})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"admin"`) {
		t.Errorf("me: %d %s", rec.Code, rec.Body)
	}
	// An authenticated client gets the JSON 404.
	rec = do(t, h, request{method: "GET", path: "/api/v1/nope", cookie: cookie})
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown path with session: %d", rec.Code)
	}

	rec = do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: `{"username":"admin","password":"wrong-password"}`})
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Set-Cookie") != "" {
		t.Errorf("bad login: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	second := sessionCookieOf(t, rec)

	// Logout ends only its own session and clears the cookie.
	rec = do(t, h, request{method: "POST", path: "/api/v1/auth/logout", cookie: second})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	if c := sessionCookieOf(t, rec); c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("logout cookie = %+v", c)
	}
	if rec := do(t, h, request{method: "GET", path: "/api/v1/auth/me", cookie: second}); rec.Code != http.StatusUnauthorized {
		t.Errorf("me after logout: %d", rec.Code)
	}
	if rec := do(t, h, request{method: "GET", path: "/api/v1/auth/me", cookie: cookie}); rec.Code != http.StatusOK {
		t.Errorf("other session after logout: %d", rec.Code)
	}
	// Logout without a session still answers 204.
	if rec := do(t, h, request{method: "POST", path: "/api/v1/auth/logout"}); rec.Code != http.StatusNoContent {
		t.Errorf("logout without session: %d", rec.Code)
	}
}

func TestSecureCookieOverTLS(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t)
	r := httptest.NewRequest(http.MethodPost, "https://nudge.example/api/v1/setup", strings.NewReader(adminCreds))
	r.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if c := sessionCookieOf(t, rec); !c.Secure {
		t.Error("cookie not Secure over TLS")
	}
}

func TestSecureCookieConfigured(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		secure bool
	}{
		{name: "configured: Secure over plain HTTP too (TLS proxy without X-Forwarded-Proto)", secure: true},
		{name: "not configured: only over HTTPS", secure: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &server{secureCookies: tt.secure}
			rec := httptest.NewRecorder()
			s.setSessionCookie(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil), "tok", time.Now().Add(time.Hour), 0)
			if c := sessionCookieOf(t, rec); c.Secure != tt.secure {
				t.Errorf("Secure = %v, want %v", c.Secure, tt.secure)
			}
		})
	}
}

func TestLoginRateLimited(t *testing.T) {
	t.Parallel()
	h, svc := newTestHandler(t)
	if _, err := svc.CreateFirstUser(context.Background(), "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	var rec *httptest.ResponseRecorder
	for range 30 {
		rec = do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: `{"username":"admin","password":"wrong-password"}`})
		if rec.Code == http.StatusTooManyRequests {
			break
		}
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("never rate limited: %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After = %q", ra)
	}
}

func TestCrossOriginWrites(t *testing.T) {
	t.Parallel()
	h, svc := newTestHandler(t)
	if _, err := svc.CreateFirstUser(context.Background(), "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		headers  map[string]string
		wantCode int
	}{
		{name: "same origin fetch metadata", headers: map[string]string{"Sec-Fetch-Site": "same-origin"}, wantCode: 200},
		{name: "cross site fetch metadata", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, wantCode: 403},
		{name: "same site is not same origin", headers: map[string]string{"Sec-Fetch-Site": "same-site"}, wantCode: 403},
		{name: "matching origin", headers: map[string]string{"Origin": "http://example.com"}, wantCode: 200},
		{name: "foreign origin", headers: map[string]string{"Origin": "https://evil.example"}, wantCode: 403},
		// Non-browser clients send neither header.
		{name: "no headers", wantCode: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// httptest.NewRequest's Host is example.com.
			rec := do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds, headers: tt.headers})
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
			if tt.wantCode == 403 && !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("403 body is not JSON: %s", rec.Body)
			}
		})
	}
}

func TestConcurrentSetup(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t)
	const n = 6
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			codes[i] = do(t, h, request{method: "POST", path: "/api/v1/setup", body: adminCreds}).Code
		})
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Errorf("code = %d", c)
		}
	}
	if created != 1 {
		t.Errorf("created = %d, want 1", created)
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()
	tests := []struct{ remote, want string }{
		{"192.0.2.7:4242", "192.0.2.7"},
		{"[::ffff:192.0.2.7]:4242", "192.0.2.7"},
		{"[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:4242", "2001:db8:1:2::/64"},
		{"[fe80::1%eth0]:4242", "fe80::/64"},
		{"pipe", "pipe"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tt.remote
		if got := clientIP(r); got != tt.want {
			t.Errorf("clientIP(%q) = %q, want %q", tt.remote, got, tt.want)
		}
	}
}

func TestMeReportsAdmin(t *testing.T) {
	t.Parallel()
	for _, admin := range []bool{true, false} {
		t.Run(fmt.Sprint(admin), func(t *testing.T) {
			t.Parallel()
			bt := newBrowseTest(t, admin)
			code, body := bt.call(t, "GET", "/api/v1/auth/me")
			want := fmt.Sprintf(`"admin":%t`, admin)
			if code != http.StatusOK || !strings.Contains(body, want) || !strings.Contains(body, `"username":"admin"`) {
				t.Errorf("me = %d %s, want %s", code, body, want)
			}
		})
	}
}
