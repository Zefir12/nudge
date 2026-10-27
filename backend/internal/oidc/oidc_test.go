package oidc

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/oidc/oidctest"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestClient(t *testing.T, mutate func(*Options)) (*Client, *oidctest.Provider, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Now().Truncate(time.Second)}
	idp := oidctest.New(t, clock.Now)
	opts := Options{
		Issuer: idp.URL, ClientID: oidctest.ClientID, ClientSecret: oidctest.ClientSecret, RedirectURL: oidctest.RedirectURL,
		AdminRole: "admin", HTTPClient: idp.Client, Now: clock.Now,
	}
	if mutate != nil {
		mutate(&opts)
	}
	return New(opts), idp, clock
}

// login runs a whole login and returns Finish's result.
func login(t *testing.T, c *Client, idp *oidctest.Provider) (Identity, error) {
	t.Helper()
	authURL, binding, err := c.Begin(t.Context(), "10.0.0.1", "/stacks")
	if err != nil {
		t.Fatal(err)
	}
	state, code := idp.Authorize(t, authURL)
	return c.Finish(t.Context(), state, binding, code)
}

func TestLogin(t *testing.T) {
	t.Parallel()
	c, idp, _ := newTestClient(t, nil)
	id, err := login(t, c, idp)
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{Issuer: idp.URL, Subject: "user-1", Username: "alice", DisplayName: "Alice", Email: "alice@example.net", Roles: []string{"admin"}, Redirect: "/stacks"}
	if id.Issuer != want.Issuer || id.Subject != want.Subject || id.Username != want.Username || id.DisplayName != want.DisplayName ||
		id.Email != want.Email || id.Redirect != want.Redirect || len(id.Roles) != 1 || !id.Admin {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
	if got, want := c.LogoutURL(), idp.URL+"/logout?client_id=nudge"; got != want {
		t.Errorf("LogoutURL = %q, want %q", got, want)
	}
}

func TestLoginRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		opts    func(*Options)
		edit    func(header, claims map[string]any)
		sign    func(input string) string
		refused string
	}{
		{name: "wrong issuer", edit: func(_, c map[string]any) { c["iss"] = "https://evil.example" }},
		{name: "wrong audience", edit: func(_, c map[string]any) { c["aud"] = "other-client" }},
		{name: "several audiences without azp", edit: func(_, c map[string]any) { c["aud"] = []string{oidctest.ClientID, "other"} }},
		{name: "azp of another client", edit: func(_, c map[string]any) { c["aud"] = []string{oidctest.ClientID, "other"}; c["azp"] = "other" }},
		{name: "wrong nonce", edit: func(_, c map[string]any) { c["nonce"] = "replayed" }},
		{name: "expired", edit: func(_, c map[string]any) { c["exp"] = c["iat"].(int64) - 1 }},
		{name: "issued in the future", edit: func(_, c map[string]any) { c["iat"] = c["iat"].(int64) + 120 }},
		{name: "alg none", edit: func(h, _ map[string]any) { h["alg"] = "none" }, sign: func(string) string { return "" }},
		{name: "HS256 with the client secret", edit: func(h, _ map[string]any) { h["alg"] = "HS256" }, sign: func(in string) string {
			m := hmac.New(sha256.New, []byte(oidctest.ClientSecret))
			m.Write([]byte(in))
			return oidctest.B64(m.Sum(nil))
		}},
		{name: "bad signature", sign: func(string) string { return oidctest.B64([]byte("not a signature")) }},
		{name: "no role", edit: func(_, c map[string]any) { c["roles"] = []string{"user"} }, refused: "no role"},
		{name: "no roles claim", edit: func(_, c map[string]any) { delete(c, "roles") }, refused: "no role"},
		{
			name: "mfa required without mfa", opts: func(o *Options) { o.RequireMFA = true },
			edit: func(_, c map[string]any) { c["amr"] = []string{"pwd", "otp"} }, refused: "no second factor",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, idp, _ := newTestClient(t, tt.opts)
			if tt.edit != nil {
				idp.Edit(tt.edit)
			}
			if tt.sign != nil {
				idp.Sign(tt.sign)
			}
			_, err := login(t, c, idp)
			var refused *RefusedError
			switch {
			case err == nil:
				t.Fatal("login succeeded")
			case tt.refused != "" && (!errors.As(err, &refused) || refused.Reason != tt.refused || refused.Subject != "user-1"):
				t.Errorf("err = %v, want refused with %q", err, tt.refused)
			case tt.refused == "" && errors.As(err, &refused):
				t.Errorf("err = %v, want a verification error", err)
			}
		})
	}
}

func TestRoles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		userRole  string
		roles     []string
		wantAdmin bool
		refused   bool
	}{
		{name: "admin", userRole: "user", roles: []string{"admin"}, wantAdmin: true},
		{name: "both is an admin", userRole: "user", roles: []string{"user", "admin"}, wantAdmin: true},
		{name: "user", userRole: "user", roles: []string{"user"}},
		{name: "other role", userRole: "user", roles: []string{"guest"}, refused: true},
		{name: "users off", userRole: "", roles: []string{"user"}, refused: true},
		// An empty roles claim never matches an empty user role.
		{name: "no roles with users off", userRole: "", roles: []string{""}, refused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, idp, _ := newTestClient(t, func(o *Options) { o.UserRole = tt.userRole })
			idp.Edit(func(_, c map[string]any) { c["roles"] = tt.roles })
			id, err := login(t, c, idp)
			var refused *RefusedError
			switch {
			case tt.refused && (!errors.As(err, &refused) || refused.Reason != "no role"):
				t.Errorf("err = %v, want refused with no role", err)
			case !tt.refused && (err != nil || id.Admin != tt.wantAdmin):
				t.Errorf("identity = %+v, %v; want admin %v", id, err, tt.wantAdmin)
			}
		})
	}
}

func TestMFA(t *testing.T) {
	t.Parallel()
	c, idp, _ := newTestClient(t, func(o *Options) { o.RequireMFA = true })
	idp.Edit(func(_, c map[string]any) { c["amr"] = []string{"pwd", "otp", "mfa"} })
	if _, err := login(t, c, idp); err != nil {
		t.Fatal(err)
	}
}

func TestState(t *testing.T) {
	t.Parallel()
	c, idp, clock := newTestClient(t, nil)
	ctx := t.Context()

	begin := func() (state, binding, code string) {
		authURL, binding, err := c.Begin(ctx, "10.0.0.1", "/")
		if err != nil {
			t.Fatal(err)
		}
		state, code = idp.Authorize(t, authURL)
		return state, binding, code
	}

	state, binding, code := begin()
	if _, err := c.Finish(ctx, "unknown", binding, code); !errors.Is(err, ErrState) {
		t.Errorf("unknown state err = %v", err)
	}
	if _, err := c.Finish(ctx, "", binding, code); !errors.Is(err, ErrState) {
		t.Errorf("missing state err = %v", err)
	}
	if _, err := c.Finish(ctx, state, binding, code); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := c.Finish(ctx, state, binding, code); !errors.Is(err, ErrState) {
		t.Errorf("replayed state err = %v", err)
	}

	// Without this browser's binding cookie the state is refused, and used up.
	state, binding, code = begin()
	if _, err := c.Finish(ctx, state, "", code); !errors.Is(err, ErrState) {
		t.Errorf("missing binding err = %v", err)
	}
	if _, err := c.Finish(ctx, state, binding, code); !errors.Is(err, ErrState) {
		t.Errorf("after a wrong binding err = %v", err)
	}

	state, binding, code = begin()
	clock.Advance(PendingTTL)
	if _, err := c.Finish(ctx, state, binding, code); !errors.Is(err, ErrState) {
		t.Errorf("expired login err = %v", err)
	}
}

func TestPendingBounded(t *testing.T) {
	t.Parallel()
	c, idp, _ := newTestClient(t, nil)
	begin := func(client string) (state, binding, code string) {
		t.Helper()
		authURL, binding, err := c.Begin(t.Context(), client, "/")
		if err != nil {
			t.Fatal(err)
		}
		state, code = idp.Authorize(t, authURL)
		return state, binding, code
	}
	pending := func() int {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.pending)
	}

	// One client floods: only its own logins go.
	victim, vBinding, vCode := begin("10.0.0.1")
	first, fBinding, fCode := begin("10.0.0.2")
	for range maxPendingPerClient + 5 {
		begin("10.0.0.2")
	}
	if n := pending(); n != maxPendingPerClient+1 {
		t.Errorf("pending logins = %d, want %d", n, maxPendingPerClient+1)
	}
	if _, err := c.Finish(t.Context(), first, fBinding, fCode); !errors.Is(err, ErrState) {
		t.Errorf("flooder's oldest login err = %v, want it dropped", err)
	}
	if _, err := c.Finish(t.Context(), victim, vBinding, vCode); err != nil {
		t.Errorf("other client's login: %v", err)
	}

	// Many clients: the total stays bounded.
	for i := range maxPending + 10 {
		if _, _, err := c.Begin(t.Context(), fmt.Sprint(i), "/"); err != nil {
			t.Fatal(err)
		}
	}
	if n := pending(); n > maxPending {
		t.Errorf("pending logins = %d, want at most %d", n, maxPending)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.perClient) > maxPending {
		t.Errorf("per-client counts = %d, want at most %d", len(c.perClient), maxPending)
	}
}

func TestUnknownKidRefetchesOnce(t *testing.T) {
	t.Parallel()
	c, idp, _ := newTestClient(t, nil)
	if _, err := login(t, c, idp); err != nil {
		t.Fatal(err)
	}
	// The provider rotates to a new kid: one refetch finds it.
	idp.Rotate("k2")
	if _, err := login(t, c, idp); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	// A kid the provider doesn't publish: at most one refetch, then refused.
	idp.Edit(func(h, _ map[string]any) { h["kid"] = "nowhere" })
	before := idp.JWKSCalls()
	if _, err := login(t, c, idp); err == nil {
		t.Fatal("unknown kid accepted")
	}
	if got := idp.JWKSCalls() - before; got > 1 {
		t.Errorf("JWKS fetched %d times for an unknown kid, want at most 1", got)
	}
}

func TestDiscovery(t *testing.T) {
	t.Parallel()
	// Discovery must report the configured issuer.
	c, idp, clock := newTestClient(t, nil)
	idp.SetIssuer("https://evil.example")
	if _, _, err := c.Begin(t.Context(), "10.0.0.1", "/"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("wrong discovered issuer err = %v", err)
	}
	idp.SetIssuer(idp.URL)
	// A failed discovery isn't retried at once.
	if _, _, err := c.Begin(t.Context(), "10.0.0.1", "/"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("immediate retry err = %v", err)
	}
	clock.Advance(rediscoverAfter)
	if _, _, err := c.Begin(t.Context(), "10.0.0.1", "/"); err != nil {
		t.Errorf("retry after the pause: %v", err)
	}
}

func TestCappedBody(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", maxBody+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(big)) }))
	defer srv.Close()
	c := New(Options{Issuer: srv.URL})
	resp, err := c.http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); !errors.Is(err, errTooLarge) {
		t.Errorf("err = %v, want errTooLarge", err)
	}
}
