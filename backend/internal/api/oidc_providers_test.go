package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/oidc/oidctest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/oidcproviders"
)

// newProvidersTest is the API with an admin (local, with a password) and the HTTP client
// of two fake providers.
func newProvidersTest(t *testing.T) (http.Handler, *http.Cookie, *oidctest.Provider, *oidctest.Provider) {
	t.Helper()
	a, b := oidctest.New(t, time.Now), oidctest.New(t, time.Now)
	h, _, _ := newTestHandlerSSO(t, func(environments.Environment) (docker.Client, error) { return &dockertest.Fake{}, nil },
		func(o *oidcproviders.Options) { o.HTTPClient = a.Client }, nil)
	if rec := do(t, h, request{method: http.MethodPost, path: "/api/v1/setup", body: adminCreds}); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("setup = %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, request{method: http.MethodPost, path: "/api/v1/auth/login", body: adminCreds})
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	return h, sessionCookieOf(t, rec), a, b
}

func providerBody(id, name, issuer string) string {
	return `{"id":"` + id + `","name":"` + name + `","issuer":"` + issuer + `","client_id":"` + oidctest.ClientID +
		`","client_secret":"` + oidctest.ClientSecret + `","admin_role":"admin","user_role":"user","session_ttl_minutes":60}`
}

func providerCallback(id string) string {
	return "https://nudge.example.net/api/v1/auth/oidc/" + id + "/callback"
}

// providerLogin signs in through provider id and returns the callback's answer.
func providerLogin(t *testing.T, h http.Handler, idp *oidctest.Provider, id string) *httptest.ResponseRecorder {
	t.Helper()
	idp.Redirect = providerCallback(id)
	rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/oidc/" + id + "/login?redirect=/x"})
	if rec.Code != http.StatusFound {
		t.Fatalf("login start = %d %s", rec.Code, rec.Body)
	}
	var binding *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcCookie {
			binding = c
		}
	}
	state, code := idp.Authorize(t, rec.Header().Get("Location"))
	return do(t, h, request{
		method: http.MethodGet, cookie: binding,
		path: "/api/v1/auth/oidc/" + id + "/callback?state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(code),
	})
}

func TestOIDCProvidersCRUD(t *testing.T) {
	t.Parallel()
	h, admin, a, _ := newProvidersTest(t)
	rec := do(t, h, request{method: http.MethodPost, path: "/api/v1/oidc-providers", cookie: admin, body: providerBody("first", "First", a.URL)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), oidctest.ClientSecret) || strings.Contains(rec.Body.String(), "client_secret") {
		t.Errorf("the answer has the secret: %s", rec.Body)
	}
	var got oidcProviderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "first" || !got.Enabled || got.ReadOnly || got.SessionTTLMinutes != 60 ||
		got.CallbackURL != "https://nudge.example.net/api/v1/auth/oidc/first/callback" {
		t.Errorf("created = %+v", got)
	}

	rec = do(t, h, request{method: http.MethodGet, path: "/api/v1/oidc-providers", cookie: admin})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), oidctest.ClientSecret) {
		t.Errorf("list = %d %s", rec.Code, rec.Body)
	}
	var list oidcProvidersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.CallbackPrefix != "https://nudge.example.net/api/v1/auth/oidc/" || len(list.Providers) != 1 {
		t.Errorf("list = %+v", list)
	}
	if rec = do(t, h, request{method: http.MethodGet, path: "/api/v1/oidc-providers/first", cookie: admin}); rec.Code != http.StatusOK {
		t.Errorf("get = %d %s", rec.Code, rec.Body)
	}

	rename := `{"name":"Renamed","issuer":"` + a.URL + `","client_id":"` + oidctest.ClientID + `","admin_role":"admin"}`
	rec = do(t, h, request{method: http.MethodPut, path: "/api/v1/oidc-providers/first", cookie: admin, body: rename})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"Renamed"`) {
		t.Errorf("update = %d %s", rec.Code, rec.Body)
	}
	// The kept secret still works after the update.
	if rec = providerLogin(t, h, a, "first"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/x" {
		t.Errorf("login after a rename = %d %s", rec.Code, rec.Header().Get("Location"))
	}

	for _, tt := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"bad input", http.MethodPost, "/api/v1/oidc-providers", providerBody("x", "X", "http://a.example.net"), http.StatusBadRequest},
		{"ttl out of range", http.MethodPost, "/api/v1/oidc-providers", strings.Replace(providerBody("x", "X", "https://x.example.net"), `:60}`, `:-5}`, 1), http.StatusBadRequest},
		{"huge ttl", http.MethodPost, "/api/v1/oidc-providers", strings.Replace(providerBody("x", "X", "https://x.example.net"), `:60}`, `:9007199254740991}`, 1), http.StatusBadRequest},
		{"taken", http.MethodPost, "/api/v1/oidc-providers", providerBody("first", "X", "https://x.example.net"), http.StatusConflict},
		{"unknown get", http.MethodGet, "/api/v1/oidc-providers/nope", "", http.StatusNotFound},
		{"unknown put", http.MethodPut, "/api/v1/oidc-providers/nope", providerBody("", "N", "https://n.example.net"), http.StatusNotFound},
		{"unknown delete", http.MethodDelete, "/api/v1/oidc-providers/nope", "", http.StatusNotFound},
	} {
		if rec := do(t, h, request{method: tt.method, path: tt.path, cookie: admin, body: tt.body}); rec.Code != tt.want {
			t.Errorf("%s = %d %s, want %d", tt.name, rec.Code, rec.Body, tt.want)
		}
	}

	if rec = do(t, h, request{method: http.MethodDelete, path: "/api/v1/oidc-providers/first", cookie: admin}); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d %s", rec.Code, rec.Body)
	}
	if rec = do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/oidc/first/login", cookie: admin}); rec.Code != http.StatusNotFound {
		t.Errorf("login at a deleted provider = %d", rec.Code)
	}
}

func TestOIDCProvidersNeedAdminSession(t *testing.T) {
	t.Parallel()
	h, admin, a, _ := newProvidersTest(t)
	user := addUser(t, h, admin, "bob", "user")
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/oidc-providers"},
		{http.MethodPost, "/api/v1/oidc-providers"},
		{http.MethodGet, "/api/v1/oidc-providers/x"},
		{http.MethodPut, "/api/v1/oidc-providers/x"},
		{http.MethodDelete, "/api/v1/oidc-providers/x"},
	} {
		if rec := do(t, h, request{method: tt.method, path: tt.path}); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d", tt.method, tt.path, rec.Code)
		}
		if rec := do(t, h, request{method: tt.method, path: tt.path, cookie: user}); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a user = %d", tt.method, tt.path, rec.Code)
		}
	}
	// An API token can read the list, but not change who signs in.
	rec := do(t, h, request{method: http.MethodPost, path: "/api/v1/tokens", cookie: admin, body: `{"name":"t"}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create token = %d %s", rec.Code, rec.Body)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil {
		t.Fatal(err)
	}
	bearer := map[string]string{"Authorization": "Bearer " + tok.Token}
	if rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/oidc-providers", headers: bearer}); rec.Code != http.StatusOK {
		t.Errorf("list with a token = %d", rec.Code)
	}
	if rec := do(t, h, request{method: http.MethodPost, path: "/api/v1/oidc-providers", headers: bearer, body: providerBody("t", "T", a.URL)}); rec.Code != http.StatusForbidden {
		t.Errorf("create with a token = %d", rec.Code)
	}
}

// TestSeveralProviders: each provider has its own button and callback, users stay apart by
// issuer, a login can't finish at another provider's callback, and #193's re-link merges
// the same person from two providers.
func TestSeveralProviders(t *testing.T) {
	t.Parallel()
	h, admin, a, b := newProvidersTest(t)
	for _, body := range []string{providerBody("beta", "Beta", b.URL), providerBody("alpha", "Alpha", a.URL)} {
		if rec := do(t, h, request{method: http.MethodPost, path: "/api/v1/oidc-providers", cookie: admin, body: body}); rec.Code != http.StatusCreated {
			t.Fatalf("create = %d %s", rec.Code, rec.Body)
		}
	}
	rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/methods"})
	want := `{"password":true,"providers":[{"id":"alpha","name":"Alpha"},{"id":"beta","name":"Beta"}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("methods = %s", got)
	}

	// A login started at alpha is unknown at beta.
	a.Redirect = providerCallback("alpha")
	rec = do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/oidc/alpha/login"})
	if rec.Code != http.StatusFound {
		t.Fatalf("login start = %d", rec.Code)
	}
	var binding *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcCookie {
			binding = c
		}
	}
	state, code := a.Authorize(t, rec.Header().Get("Location"))
	rec = do(t, h, request{
		method: http.MethodGet, cookie: binding,
		path: "/api/v1/auth/oidc/beta/callback?state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(code),
	})
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != ssoFailedPath {
		t.Errorf("wrong callback = %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// Both logins with the same subject: two users.
	userIDs := map[string]int64{}
	for id, idp := range map[string]*oidctest.Provider{"alpha": a, "beta": b} {
		rec := providerLogin(t, h, idp, id)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/x" {
			t.Fatalf("%s login = %d %s", id, rec.Code, rec.Header().Get("Location"))
		}
		me := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/me", cookie: sessionCookieOf(t, rec)})
		var u struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(me.Body.Bytes(), &u); err != nil {
			t.Fatal(err)
		}
		userIDs[id] = u.ID
	}
	if userIDs["alpha"] == userIDs["beta"] {
		t.Fatalf("the same user for two issuers: %v", userIDs)
	}
	rec = do(t, h, request{
		method: http.MethodPost, cookie: admin,
		path: "/api/v1/users/" + strconv.FormatInt(userIDs["alpha"], 10) + "/relink", body: `{"duplicate":` + strconv.FormatInt(userIDs["beta"], 10) + `}`,
	})
	if rec.Code != http.StatusOK {
		t.Errorf("relink = %d %s", rec.Code, rec.Body)
	}
}

func TestDisablingAProviderRevokesItsUsers(t *testing.T) {
	t.Parallel()
	h, admin, a, _ := newProvidersTest(t)
	if rec := do(t, h, request{method: http.MethodPost, path: "/api/v1/oidc-providers", cookie: admin, body: providerBody("first", "First", a.URL)}); rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	rec := providerLogin(t, h, a, "first")
	if rec.Code != http.StatusFound {
		t.Fatalf("login = %d", rec.Code)
	}
	user := sessionCookieOf(t, rec)
	if rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/me", cookie: user}); rec.Code != http.StatusOK {
		t.Fatalf("me = %d", rec.Code)
	}
	body := `{"name":"First","issuer":"` + a.URL + `","client_id":"` + oidctest.ClientID + `","admin_role":"admin","enabled":false}`
	if rec := do(t, h, request{method: http.MethodPut, path: "/api/v1/oidc-providers/first", cookie: admin, body: body}); rec.Code != http.StatusOK {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/me", cookie: user}); rec.Code != http.StatusUnauthorized {
		t.Errorf("session after disabling = %d, want 401", rec.Code)
	}
	if rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/oidc/first/login"}); rec.Code != http.StatusNotFound {
		t.Errorf("login at a disabled provider = %d", rec.Code)
	}
	if got := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/methods"}).Body.String(); strings.Contains(got, "first") {
		t.Errorf("methods offer a disabled provider: %s", got)
	}
	rec = do(t, h, request{method: http.MethodGet, path: "/api/v1/audit", cookie: admin})
	if !strings.Contains(rec.Body.String(), "first; revoked 1 sessions, 0 API tokens") {
		t.Errorf("audit lacks the revocation: %s", rec.Body)
	}
}

// TestEnvProviderReadOnly: the NUDGE_OIDC_* provider is listed, without its secret, on its
// old callback URL, and can't be changed from the API.
func TestEnvProviderReadOnly(t *testing.T) {
	t.Parallel()
	h, _, _, idp := newSSOHandler(t, nil)
	admin := sessionCookieOf(t, ssoLogin(t, h, idp, "/", true))
	for _, tt := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/v1/oidc-providers/env", providerBody("", "x", idp.URL)},
		{http.MethodDelete, "/api/v1/oidc-providers/env", ""},
	} {
		if rec := do(t, h, request{method: tt.method, path: tt.path, cookie: admin, body: tt.body}); rec.Code != http.StatusConflict {
			t.Errorf("%s env provider = %d %s, want 409", tt.method, rec.Code, rec.Body)
		}
	}
	rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/oidc-providers", cookie: admin})
	if !strings.Contains(rec.Body.String(), `"read_only":true`) || !strings.Contains(rec.Body.String(), `"callback_url":"https://nudge.example.net/api/v1/auth/oidc/callback"`) {
		t.Errorf("list = %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), oidctest.ClientSecret) {
		t.Errorf("list has the env secret: %s", rec.Body)
	}
	// The env provider also answers at its id.
	if rec := do(t, h, request{method: http.MethodGet, path: "/api/v1/auth/oidc/env/login"}); rec.Code != http.StatusFound {
		t.Errorf("env login by id = %d", rec.Code)
	}
}
