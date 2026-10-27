package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

func newSSOService(t *testing.T, opts Options) (*Service, *fakeClock) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	opts.DB, opts.Now, opts.Params = st.DB(), clock.Now, testParams
	return New(opts), clock
}

func TestSSOLogin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock := newSSOService(t, Options{SSOSessionTTL: 12 * time.Hour})
	local, err := s.CreateFirstUser(ctx, "zefir", "longenough")
	if err != nil {
		t.Fatal(err)
	}

	id := Identity{Issuer: "https://auth.example.net", Subject: "u-1", Username: "zefir", DisplayName: "Zefir", Email: "zefir@example.net", Role: RoleAdmin}
	sess, err := s.SSOLogin(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Same name as a local user: a new user, never linked.
	if sess.User.ID == local.ID || sess.User.Username != "zefir-2" || !sess.User.SSO {
		t.Errorf("first login user = %+v", sess.User)
	}
	if want := clock.Now().Add(12 * time.Hour); !sess.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v (the SSO TTL)", sess.ExpiresAt, want)
	}
	if got, err := s.SessionUser(ctx, sess.Token); err != nil || !got.SSO || got.ID != sess.User.ID {
		t.Errorf("SessionUser = %+v, %v", got, err)
	}

	// The next login finds the user by issuer and subject and refreshes the profile.
	id.DisplayName, id.Email, id.Username = "Zefir L.", "z@example.net", "other"
	again, err := s.SSOLogin(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if again.User.ID != sess.User.ID || again.User.Username != "zefir-2" {
		t.Errorf("second login user = %+v, want the first one", again.User)
	}
	var display, email string
	if err := s.db.QueryRow(`SELECT display_name, email FROM users WHERE id = ?`, again.User.ID).Scan(&display, &email); err != nil || display != "Zefir L." || email != "z@example.net" {
		t.Errorf("profile = %q %q, %v", display, email, err)
	}

	// The role is the provider's on every login; a disabled user is refused.
	if sess.User.Role != RoleAdmin {
		t.Errorf("first login role = %q", sess.User.Role)
	}
	id.Role = RoleUser
	if again, err = s.SSOLogin(ctx, id); err != nil || again.User.Role != RoleUser {
		t.Errorf("login with the user role = %+v, %v", again.User, err)
	}
	if got, err := s.SessionUser(ctx, sess.Token); err != nil || got.Role != RoleUser {
		t.Errorf("older session's role = %+v, %v; want the new one", got, err)
	}
	if _, err := s.SSOLogin(ctx, Identity{Issuer: "https://auth.example.net", Subject: "u-1"}); err == nil {
		t.Error("login without a role succeeded")
	}
	if _, err := s.db.Exec(`UPDATE users SET disabled = 1 WHERE id = ?`, sess.User.ID); err != nil {
		t.Fatal(err)
	}
	id.Role = RoleAdmin
	if _, err := s.SSOLogin(ctx, id); !errors.Is(err, ErrDisabled) {
		t.Errorf("disabled user's login err = %v, want ErrDisabled", err)
	}
	var role Role
	if err := s.db.QueryRow(`SELECT role FROM users WHERE id = ?`, sess.User.ID).Scan(&role); err != nil || role != RoleUser {
		t.Errorf("refused login changed the role to %q, %v", role, err)
	}
	id.Role = RoleUser
	if _, err := s.SessionUser(ctx, sess.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("disabled user's session err = %v", err)
	}
	if _, err := s.db.Exec(`UPDATE users SET disabled = 0 WHERE id = ?`, sess.User.ID); err != nil {
		t.Fatal(err)
	}

	// The same subject from another issuer is someone else.
	other, err := s.SSOLogin(ctx, Identity{Issuer: "https://other.example.net", Subject: "u-1", Email: "zefir@example.net", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if other.User.ID == sess.User.ID || other.User.Username != "zefir@example.net" {
		t.Errorf("other issuer user = %+v", other.User)
	}

	// SSO users have no password.
	if _, err := s.Login(ctx, "10.0.0.1", "zefir-2", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("password login of an SSO user err = %v", err)
	}
	if sso, err := s.Logout(ctx, sess.Token); err != nil || !sso {
		t.Errorf("Logout = %v, %v; want an SSO session", sso, err)
	}
	if sso, err := s.Logout(ctx, "unknown"); err != nil || sso {
		t.Errorf("Logout(unknown) = %v, %v", sso, err)
	}
}

func TestSSOUsernames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tests := []struct {
		name string
		id   Identity
		want string
	}{
		{name: "preferred username", id: Identity{Subject: "a", Username: "alice", Email: "a@example.net"}, want: "alice"},
		{name: "email when no username", id: Identity{Subject: "b", Email: "b@example.net"}, want: "b@example.net"},
		{name: "never the break-glass admin", id: Identity{Subject: "c", Username: "Admin"}, want: "sso-"},
		{name: "invalid username skipped", id: Identity{Subject: "d", Username: "bad\nname", Email: "d@example.net"}, want: "d@example.net"},
		{name: "from the subject", id: Identity{Subject: "e"}, want: "sso-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _ := newSSOService(t, Options{})
			tt.id.Issuer, tt.id.Role = "https://auth.example.net", RoleUser
			sess, err := s.SSOLogin(ctx, tt.id)
			if err != nil {
				t.Fatal(err)
			}
			// A want ending in "-" is a prefix (the rest is derived or random).
			got := sess.User.Username
			if ok := got == tt.want || strings.HasSuffix(tt.want, "-") && strings.HasPrefix(got, tt.want); !ok {
				t.Errorf("username = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSSOLoginConcurrentFirstLogin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newSSOService(t, Options{})
	id := Identity{Issuer: "https://auth.example.net", Subject: "u-1", Username: "alice", Role: RoleUser}
	var wg sync.WaitGroup
	ids := make([]int64, 8)
	for i := range ids {
		wg.Go(func() {
			sess, err := s.SSOLogin(ctx, id)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = sess.User.ID
		})
	}
	wg.Wait()
	for _, got := range ids {
		if got != ids[0] {
			t.Fatalf("user IDs = %v, want one user", ids)
		}
	}
}

func TestAdminPasswordOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newSSOService(t, Options{AdminPasswordOnly: true})
	if _, err := s.EnsureAdmin(ctx, "longenough"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO users (username, password_hash, created_at, updated_at) SELECT 'bob', password_hash, 0, 0 FROM users`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "10.0.0.1", "bob", "longenough"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("bob err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := s.Login(ctx, "10.0.0.1", "ADMIN", "longenough"); err != nil {
		t.Errorf("break-glass admin err = %v", err)
	}
}
