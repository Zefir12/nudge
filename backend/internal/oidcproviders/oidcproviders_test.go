package oidcproviders

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/oidc/oidctest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

type env struct {
	svc  *Service
	auth *auth.Service
	st   *store.Store
	box  *secretbox.Box
	opts Options
}

func newEnv(t *testing.T, mod func(*Options)) *env {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	authSvc := auth.New(auth.Options{DB: st.DB(), Params: auth.Params{Memory: 64, Time: 1, Threads: 1}})
	opts := Options{DB: st.DB(), Box: box, Auth: authSvc, PublicURL: "https://nudge.example.net"}
	if mod != nil {
		mod(&opts)
	}
	svc := New(opts)
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, auth: authSvc, st: st, box: box, opts: opts}
}

func ptr[T any](v T) *T { return &v }

func input(name, issuer string) Input {
	return Input{Name: name, Issuer: issuer, ClientID: "nudge", ClientSecret: ptr("s3cret"), AdminRole: "admin", UserRole: "user"}
}

// localAdmin gives the instance a way in that no provider change can take away.
func (e *env) localAdmin(t *testing.T) {
	t.Helper()
	if _, err := e.auth.CreateFirstUser(context.Background(), "root", "longenoughpassword"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateValidates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mod  func(*Input)
	}{
		{"no name", func(i *Input) { i.Name = " " }},
		{"control characters in the name", func(i *Input) { i.Name = "a\nb" }},
		{"no client id", func(i *Input) { i.ClientID = "" }},
		{"no secret", func(i *Input) { i.ClientSecret = nil }},
		{"empty secret", func(i *Input) { i.ClientSecret = ptr("") }},
		{"no admin role", func(i *Input) { i.AdminRole = "" }},
		{"same roles", func(i *Input) { i.UserRole = i.AdminRole }},
		{"http issuer", func(i *Input) { i.Issuer = "http://a.example.net" }},
		{"issuer with a query", func(i *Input) { i.Issuer = "https://a.example.net?x=1" }},
		{"issuer with a login", func(i *Input) { i.Issuer = "https://u:p@a.example.net" }},
		{"bad id", func(i *Input) { i.ID = "A/B" }},
		{"name without an id in it", func(i *Input) { i.Name = "!!!" }},
		{"ttl too short", func(i *Input) { i.SessionTTL = time.Minute }},
		{"ttl too long", func(i *Input) { i.SessionTTL = 31 * 24 * time.Hour }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, nil)
			in := input("Authestra", "https://a.example.net")
			tt.mod(&in)
			var inputErr *InputError
			if _, err := e.svc.Create(context.Background(), in); !errors.As(err, &inputErr) {
				t.Errorf("err = %v, want an InputError", err)
			}
		})
	}
}

func TestCreateNeedsHTTPSPublicURL(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *Options) { o.PublicURL = "http://nudge.example.net" })
	var inputErr *InputError
	if _, err := e.svc.Create(context.Background(), input("A", "https://a.example.net")); !errors.As(err, &inputErr) {
		t.Errorf("err = %v, want an InputError", err)
	}
	if got := e.svc.CallbackPrefix(); got != "" {
		t.Errorf("callback prefix = %q", got)
	}
}

func TestCreateDefaultsAndConflicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, func(o *Options) {
		o.Env = &Env{Name: "Env", Issuer: "https://env.example.net", ClientID: "n", ClientSecret: "x", AdminRole: "admin", SessionTTL: time.Hour}
	})
	p, err := e.svc.Create(ctx, input("My Auth!", "https://a.example.net"))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "my-auth" || p.SessionTTL != DefaultSessionTTL || !p.Enabled || p.ReadOnly {
		t.Errorf("provider = %+v", p)
	}
	if want := "https://nudge.example.net/api/v1/auth/oidc/my-auth/callback"; p.CallbackURL != want {
		t.Errorf("callback = %q, want %q", p.CallbackURL, want)
	}
	for name, in := range map[string]Input{
		"same id":                 {ID: "my-auth", Name: "B", Issuer: "https://b.example.net"},
		"same name":               {Name: "MY AUTH!", ID: "b", Issuer: "https://b.example.net"},
		"same issuer":             {Name: "B", Issuer: "https://a.example.net"},
		"issuer with a slash":     {Name: "B", Issuer: "https://a.example.net/"},
		"the reserved id":         {ID: EnvID, Name: "B", Issuer: "https://b.example.net"},
		"the env provider issuer": {Name: "B", Issuer: "https://env.example.net"},
		"the env provider name":   {Name: "env", ID: "b", Issuer: "https://b.example.net"},
	} {
		in.ClientID, in.ClientSecret, in.AdminRole = "nudge", ptr("s"), "admin"
		if _, err := e.svc.Create(ctx, in); !errors.Is(err, ErrTaken) {
			t.Errorf("%s: err = %v, want ErrTaken", name, err)
		}
	}
	list, err := e.svc.List(ctx)
	if err != nil || len(list) != 2 || list[0].ID != EnvID || !list[0].ReadOnly || list[1].ID != "my-auth" {
		t.Errorf("list = %+v, %v", list, err)
	}
}

// TestSecretSealed: the secret isn't stored in the clear, and is bound to the provider's
// id and issuer, so a database edit can't send it to another server.
func TestSecretSealed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, nil)
	if _, err := e.svc.Create(ctx, input("A", "https://a.example.net")); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err := e.st.DB().QueryRow(`SELECT client_secret FROM oidc_providers`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("s3cret")) {
		t.Error("the secret is stored in the clear")
	}
	if got, err := e.box.Open(sealed, purpose("a", "https://a.example.net")); err != nil || string(got) != "s3cret" {
		t.Errorf("open = %q, %v", got, err)
	}
	if _, err := e.st.DB().Exec(`UPDATE oidc_providers SET issuer = 'https://evil.example.net'`); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.Get(ctx, "a")
	if err != nil || !p.SecretUnreadable {
		t.Errorf("after a database edit: %+v, %v", p, err)
	}
	restarted := New(e.opts)
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := restarted.Login("a"); ok {
		t.Error("a provider with an unreadable secret can be logged in with")
	}
}

func TestUpdateSecretRules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, nil)
	e.localAdmin(t)
	if _, err := e.svc.Create(ctx, input("A", "https://a.example.net")); err != nil {
		t.Fatal(err)
	}
	in := input("A renamed", "https://a.example.net")
	in.ClientSecret = nil
	c, err := e.svc.Update(ctx, "a", in)
	if err != nil || c.Provider.Name != "A renamed" || c.Provider.ID != "a" {
		t.Fatalf("rename = %+v, %v", c, err)
	}
	var inputErr *InputError
	moved := in
	moved.Issuer = "https://evil.example.net"
	if _, err := e.svc.Update(ctx, "a", moved); !errors.As(err, &inputErr) {
		t.Errorf("new issuer without the secret: err = %v", err)
	}
	moved = in
	moved.ClientID = "other"
	if _, err := e.svc.Update(ctx, "a", moved); !errors.As(err, &inputErr) {
		t.Errorf("new client ID without the secret: err = %v", err)
	}
	moved = in
	moved.Issuer, moved.ClientSecret = "https://b.example.net", ptr("new")
	c, err = e.svc.Update(ctx, "a", moved)
	if err != nil || c.Provider.SecretUnreadable || c.Provider.Issuer != "https://b.example.net" {
		t.Errorf("new issuer with the secret = %+v, %v", c, err)
	}
	if _, err := e.svc.Update(ctx, "nope", in); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown provider err = %v", err)
	}
}

func TestEnvProviderIsReadOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, func(o *Options) {
		o.Env = &Env{Name: "Env", Issuer: "https://env.example.net", ClientID: "n", ClientSecret: "x", AdminRole: "admin", SessionTTL: time.Hour}
	})
	if _, err := e.svc.Update(ctx, EnvID, input("B", "https://b.example.net")); !errors.Is(err, ErrReadOnly) {
		t.Errorf("update err = %v", err)
	}
	if _, err := e.svc.Delete(ctx, EnvID); !errors.Is(err, ErrReadOnly) {
		t.Errorf("delete err = %v", err)
	}
	p, err := e.svc.Get(ctx, EnvID)
	if err != nil || p.CallbackURL != "https://nudge.example.net/api/v1/auth/oidc/callback" {
		t.Errorf("env provider = %+v, %v", p, err)
	}
	if l, ok := e.svc.Login(EnvID); !ok || l.Provider.Name != "Env" {
		t.Errorf("login = %+v, %v", l, ok)
	}
}

func TestTakingAwayRevokes(t *testing.T) {
	t.Parallel()
	const issuer = "https://a.example.net"
	id := auth.Identity{Issuer: issuer, Subject: "u", Username: "u", Role: auth.RoleUser}
	tests := []struct {
		name string
		act  func(ctx context.Context, s *Service) (Change, error)
		// keeps says the sessions go on.
		keeps bool
	}{
		{name: "disable", act: func(ctx context.Context, s *Service) (Change, error) {
			in := input("A", issuer)
			in.ClientSecret, in.Enabled = nil, ptr(false)
			return s.Update(ctx, "a", in)
		}},
		{name: "delete", act: func(ctx context.Context, s *Service) (Change, error) { return s.Delete(ctx, "a") }},
		{name: "another issuer", act: func(ctx context.Context, s *Service) (Change, error) {
			return s.Update(ctx, "a", input("A", "https://b.example.net"))
		}},
		{name: "a role change", keeps: true, act: func(ctx context.Context, s *Service) (Change, error) {
			in := input("A", issuer)
			in.ClientSecret, in.AdminRole = nil, "boss"
			return s.Update(ctx, "a", in)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			e := newEnv(t, nil)
			e.localAdmin(t)
			if _, err := e.svc.Create(ctx, input("A", issuer)); err != nil {
				t.Fatal(err)
			}
			sess, err := e.auth.SSOLogin(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			_, secret, err := e.auth.CreateToken(ctx, sess.User.ID, "t")
			if err != nil {
				t.Fatal(err)
			}
			before, _ := e.svc.Login("a")
			c, err := tt.act(ctx, e.svc)
			if err != nil {
				t.Fatal(err)
			}
			_, sessErr := e.auth.SessionUser(ctx, sess.Token)
			_, tokErr := e.auth.TokenUser(ctx, secret)
			if tt.keeps {
				if sessErr != nil || tokErr != nil || c.Revoked != (auth.IssuerRevoked{}) {
					t.Errorf("session %v, token %v, revoked %+v; want both kept", sessErr, tokErr, c.Revoked)
				}
			} else {
				if sessErr == nil || tokErr == nil || c.Revoked != (auth.IssuerRevoked{Sessions: 1, Tokens: 1}) {
					t.Errorf("session %v, token %v, revoked %+v; want both gone", sessErr, tokErr, c.Revoked)
				}
				if accounts, err := e.auth.ListUsers(ctx); err != nil || len(accounts) != 2 {
					t.Errorf("users = %+v, %v; the account must stay", accounts, err)
				}
			}
			// Any change replaces the client, so a login in progress can't finish.
			if err := e.svc.Complete(before, func() error { t.Error("start ran"); return nil }); !errors.Is(err, ErrGone) {
				t.Errorf("complete err = %v, want ErrGone", err)
			}
		})
	}
}

func TestDisabledProviderHasNoLogin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, nil)
	e.localAdmin(t)
	in := input("A", "https://a.example.net")
	in.Enabled = ptr(false)
	if _, err := e.svc.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.svc.Login("a"); ok || e.svc.AnyEnabled() || len(e.svc.Enabled()) != 0 {
		t.Error("a disabled provider is offered")
	}
	in.ClientSecret, in.Enabled = nil, ptr(true)
	if _, err := e.svc.Update(ctx, "a", in); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.svc.Login("a"); !ok || !e.svc.AnyEnabled() {
		t.Error("an enabled provider isn't offered")
	}
	// A missing Enabled leaves it as it is.
	in.Enabled = nil
	if c, err := e.svc.Update(ctx, "a", in); err != nil || !c.Provider.Enabled {
		t.Errorf("update = %+v, %v", c, err)
	}
}

func TestLockoutRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const a, b = "https://a.example.net", "https://b.example.net"
	e := newEnv(t, nil)
	for name, issuer := range map[string]string{"A": a, "B": b} {
		if _, err := e.svc.Create(ctx, input(name, issuer)); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := e.auth.SSOLogin(ctx, auth.Identity{Issuer: a, Subject: "u", Username: "u", Role: auth.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	// The only admin signs in through A.
	disable := input("A", a)
	disable.ClientSecret, disable.Enabled = nil, ptr(false)
	if _, err := e.svc.Update(ctx, "a", disable); !errors.Is(err, ErrNoAdminLeft) {
		t.Errorf("disable err = %v", err)
	}
	if _, err := e.svc.Delete(ctx, "a"); !errors.Is(err, ErrNoAdminLeft) {
		t.Errorf("delete err = %v", err)
	}
	if _, err := e.svc.Update(ctx, "a", input("A", "https://c.example.net")); !errors.Is(err, ErrNoAdminLeft) {
		t.Errorf("move err = %v", err)
	}
	if _, err := e.auth.SessionUser(ctx, sess.Token); err != nil {
		t.Errorf("a refused change revoked the session: %v", err)
	}
	if _, ok := e.svc.Login("a"); !ok {
		t.Error("a refused change disabled the provider")
	}
	// B goes freely, and with an admin at B too, so does A.
	if _, err := e.auth.SSOLogin(ctx, auth.Identity{Issuer: b, Subject: "v", Username: "v", Role: auth.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Delete(ctx, "a"); err != nil {
		t.Errorf("delete with another admin: %v", err)
	}
}

func TestLastProviderWithPasswordLoginOff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, func(o *Options) { o.PasswordLoginOff = true })
	if _, err := e.svc.Create(ctx, input("A", "https://a.example.net")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Delete(ctx, "a"); !errors.Is(err, ErrLastProvider) {
		t.Errorf("err = %v, want ErrLastProvider", err)
	}
}

func TestCompleteWaitsForAChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, nil)
	e.localAdmin(t)
	if _, err := e.svc.Create(ctx, input("A", "https://a.example.net")); err != nil {
		t.Fatal(err)
	}
	l, ok := e.svc.Login("a")
	if !ok {
		t.Fatal("no login")
	}
	id := auth.Identity{Issuer: "https://a.example.net", Subject: "u", Username: "u", Role: auth.RoleUser}
	done := make(chan error, 1)
	var sess auth.Session
	err := e.svc.Complete(l, func() error {
		go func() {
			_, err := e.svc.Delete(ctx, "a")
			done <- err
		}()
		var err error
		sess, err = e.auth.SSOLogin(ctx, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := e.auth.SessionUser(ctx, sess.Token); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("session started during the delete: err = %v", err)
	}
}

func TestLogoutURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	idp := oidctest.New(t, time.Now)
	e := newEnv(t, func(o *Options) { o.HTTPClient = idp.Client })
	if got := e.svc.LogoutURL(""); got != "" {
		t.Errorf("no issuer: %q", got)
	}
	if _, err := e.svc.Create(ctx, input("A", idp.URL)); err != nil {
		t.Fatal(err)
	}
	if got := e.svc.LogoutURL("https://other.example.net"); got != "" {
		t.Errorf("unknown issuer: %q", got)
	}
}

func TestDeriveID(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Authestra":                    "authestra",
		"  My  Auth! ":                 "my-auth",
		"!!!":                          "",
		"Zażółć 2":                     "za-2",
		strings.Repeat("a", 60) + " b": strings.Repeat("a", 40),
	} {
		if got := DeriveID(in); got != want {
			t.Errorf("DeriveID(%q) = %q, want %q", in, got, want)
		}
	}
}
