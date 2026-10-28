package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/oidc"
	"git.zefirlabs.net/zefir/nudge/backend/internal/oidc/oidctest"
)

// newSSOHandler is the API with SSO through a fake provider.
func newSSOHandler(t *testing.T, mod func(*oidc.Options)) (http.Handler, *auth.Service, *sql.DB, *oidctest.Provider) {
	t.Helper()
	idp := oidctest.New(t, time.Now)
	opts := oidc.Options{
		Issuer: idp.URL, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret, RedirectURL: oidctest.RedirectURL,
		AdminRole: "admin", HTTPClient: idp.Client,
	}
	if mod != nil {
		mod(&opts)
	}
	h, svc, db := newTestHandlerOpts(t, func(environments.Environment) (docker.Client, error) { return &dockertest.Fake{}, nil }, func(o *Options) {
		o.OIDC, o.OIDCName, o.NoPasswordLogin = oidc.New(opts), "Authestra", true
	})
	return h, svc, db, idp
}

// ssoLogin starts a login at nudge, has the provider grant it, and returns the
// callback's answer. Without bind the browser lost its binding cookie.
func ssoLogin(t *testing.T, h http.Handler, idp *oidctest.Provider, redirect string, bind bool) *httptest.ResponseRecorder {
	t.Helper()
	rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/oidc/login?redirect=" + url.QueryEscape(redirect)})
	if rec.Code != http.StatusFound {
		t.Fatalf("login start code = %d, body %s", rec.Code, rec.Body)
	}
	var binding *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcCookie {
			binding = c
		}
	}
	if binding == nil || !binding.HttpOnly || !binding.Secure || binding.SameSite != http.SameSiteLaxMode || binding.Path != oidcCookiePath {
		t.Fatalf("binding cookie = %+v", binding)
	}
	state, code := idp.Authorize(t, rec.Header().Get("Location"))
	req := request{method: http.MethodGet, path: "/api/v1/auth/oidc/callback?state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(code)}
	if bind {
		req.cookie = binding
	}
	return do(t, h, req)
}

func TestAuthMethods(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t)
	rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/methods"})
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != `{"password":true,"oidc":null}` {
		t.Errorf("without SSO: %d %s", rec.Code, got)
	}
	h, _, _, _ = newSSOHandler(t, nil)
	rec = do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/methods"})
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != `{"password":false,"oidc":{"name":"Authestra"}}` {
		t.Errorf("with SSO: %d %s", rec.Code, got)
	}
}

func TestSSOLogin(t *testing.T) {
	t.Parallel()
	h, svc, db, idp := newSSOHandler(t, nil)
	// A local user with the provider's username is not the SSO user.
	if _, err := svc.CreateFirstUser(context.Background(), "alice", "longenough"); err != nil {
		t.Fatal(err)
	}

	rec := ssoLogin(t, h, idp, "/env/local/stacks", true)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/env/local/stacks" {
		t.Fatalf("callback = %d to %q, body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	session := sessionCookieOf(t, rec)
	if session.SameSite != http.SameSiteStrictMode || !session.HttpOnly {
		t.Errorf("session cookie = %+v, want HttpOnly and SameSite=Strict", session)
	}
	me := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/me", cookie: session})
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"username":"alice-2"`) {
		t.Errorf("me = %d %s, want the new SSO user alice-2", me.Code, me.Body)
	}
	var username, target string
	var status int
	if err := db.QueryRow(`SELECT username, target, status FROM audit_log WHERE action = 'GET /api/v1/auth/oidc/callback'`).Scan(&username, &target, &status); err != nil ||
		username != "alice-2" || target != "user-1" || status != http.StatusFound {
		t.Errorf("audit = %q %q %d, %v", username, target, status, err)
	}

	// Logout names the provider's sign-out page.
	out := do(t, h, request{method: http.MethodPost, path: "/api/v1/auth/logout", cookie: session})
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"logout_url":"`+idp.URL+`/logout?client_id=nudge"`) {
		t.Errorf("logout = %d %s", out.Code, out.Body)
	}

	// A redirect off the site is dropped.
	rec = ssoLogin(t, h, idp, "//evil.example/x", true)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Errorf("redirect to another site: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSSOLoginFailures(t *testing.T) {
	t.Parallel()
	h, _, db, idp := newSSOHandler(t, nil)
	noSession := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		for _, c := range rec.Result().Cookies() {
			if c.Name == sessionCookie {
				t.Errorf("session cookie set: %+v", c)
			}
		}
	}

	rec := ssoLogin(t, h, idp, "/", false)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != ssoFailedPath {
		t.Errorf("without binding cookie: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	noSession(t, rec)

	rec = do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/oidc/callback?error=access_denied&state=x"})
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != ssoFailedPath {
		t.Errorf("provider error: %d to %q", rec.Code, rec.Header().Get("Location"))
	}

	idp.Edit(func(_, c map[string]any) { c["roles"] = []string{"user"} })
	rec = ssoLogin(t, h, idp, "/", true)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Access denied") {
		t.Errorf("no role: %d %s", rec.Code, rec.Body)
	}
	noSession(t, rec)
	var target string
	var status int
	if err := db.QueryRow(`SELECT target, status FROM audit_log WHERE action = 'GET /api/v1/auth/oidc/callback'`).Scan(&target, &status); err != nil ||
		target != "user-1: no role" || status != http.StatusForbidden {
		t.Errorf("audit = %q %d, %v", target, status, err)
	}
}

// TestSSOLostRole signs in an admin with a session and an API token. A login refused for
// a missing second factor keeps both; one refused because the provider took the role
// away revokes both, audited under the nudge user.
func TestSSOLostRole(t *testing.T) {
	t.Parallel()
	h, svc, db, idp := newSSOHandler(t, func(o *oidc.Options) { o.RequireMFA = true })
	idp.Edit(func(_, c map[string]any) { c["amr"] = []string{"pwd", "mfa"} })
	session := sessionCookieOf(t, ssoLogin(t, h, idp, "/", true))
	users, err := svc.ListUsers(context.Background())
	if err != nil || len(users) != 1 {
		t.Fatalf("users = %+v, %v", users, err)
	}
	_, secret, err := svc.CreateToken(context.Background(), users[0].ID, "ci")
	if err != nil {
		t.Fatal(err)
	}
	bearer := map[string]string{"Authorization": "Bearer " + secret}
	access := func(t *testing.T, want int) {
		t.Helper()
		if rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/me", cookie: session}); rec.Code != want {
			t.Errorf("session = %d, want %d", rec.Code, want)
		}
		if rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/environments", headers: bearer}); rec.Code != want {
			t.Errorf("token = %d, want %d", rec.Code, want)
		}
	}
	access(t, http.StatusOK)

	idp.Edit(nil)
	if rec := ssoLogin(t, h, idp, "/", true); rec.Code != http.StatusForbidden {
		t.Fatalf("no second factor: %d %s", rec.Code, rec.Body)
	}
	access(t, http.StatusOK)

	idp.Edit(func(_, c map[string]any) { c["roles"], c["amr"] = []string{"none"}, []string{"mfa"} })
	if rec := ssoLogin(t, h, idp, "/", true); rec.Code != http.StatusForbidden {
		t.Fatalf("no role: %d %s", rec.Code, rec.Body)
	}
	access(t, http.StatusUnauthorized)
	var username, target string
	if err := db.QueryRow(`SELECT username, target FROM audit_log WHERE target LIKE '%no role%'`).Scan(&username, &target); err != nil ||
		username != users[0].Username || target != "user-1: no role; revoked 1 sessions, 1 API tokens" {
		t.Errorf("audit = %q %q, %v", username, target, err)
	}
}

// TestSSOUserRole signs in with the provider's user role: a nudge user, refused on
// admin routes; a later login with the admin role makes them an admin, and a disabled
// one is refused.
func TestSSOUserRole(t *testing.T) {
	t.Parallel()
	h, svc, db, idp := newSSOHandler(t, func(o *oidc.Options) { o.UserRole = "user" })
	idp.Edit(func(_, c map[string]any) { c["roles"] = []string{"user"} })
	session := sessionCookieOf(t, ssoLogin(t, h, idp, "/", true))
	if me := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/me", cookie: session}); !strings.Contains(me.Body.String(), `"role":"user"`) {
		t.Errorf("me = %d %s", me.Code, me.Body)
	}
	if rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/registries", cookie: session}); rec.Code != http.StatusForbidden {
		t.Errorf("user on an admin route = %d", rec.Code)
	}

	idp.Edit(func(_, c map[string]any) { c["roles"] = []string{"admin"} })
	session = sessionCookieOf(t, ssoLogin(t, h, idp, "/", true))
	if rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/environments", cookie: session}); rec.Code != http.StatusOK {
		t.Errorf("admin on an admin route = %d %s", rec.Code, rec.Body)
	}

	users, err := svc.ListUsers(context.Background())
	if err != nil || len(users) != 1 {
		t.Fatalf("users = %+v, %v", users, err)
	}
	if _, err := db.Exec(`UPDATE users SET disabled = 1`); err != nil {
		t.Fatal(err)
	}
	rec := ssoLogin(t, h, idp, "/", true)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "disabled") {
		t.Errorf("disabled: %d %s", rec.Code, rec.Body)
	}
}

func TestSetupRefusedWithSSO(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newSSOHandler(t, nil)
	rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/setup"})
	if got := strings.TrimSpace(rec.Body.String()); got != `{"needed":false}` {
		t.Errorf("setup status = %s", got)
	}
	rec = do(t, h, request{method: http.MethodPost, path: "/api/v1/setup", body: adminCreds})
	if rec.Code != http.StatusForbidden {
		t.Errorf("setup = %d %s, want 403", rec.Code, rec.Body)
	}
}

func TestSSOOffRoutes(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t)
	for _, path := range []string{"/api/v1/auth/oidc/login", "/api/v1/auth/oidc/callback?state=x&code=y"} {
		if rec := do(t, h, request{method: http.MethodGet, path: path}); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 without SSO", path, rec.Code)
		}
	}
}

func TestSafeRedirect(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"/stacks?x=1":      "/stacks?x=1",
		"":                 "/",
		"stacks":           "/",
		"//evil.example":   "/",
		"/\\evil.example":  "/",
		"https://evil.ex/": "/",
		"/a\r\nSet-Cookie": "/",
		"/" + strings.Repeat("a", maxRedirectLen): "/",
	} {
		if got := safeRedirect(in); got != want {
			t.Errorf("safeRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}
