package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestCreateUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	first, err := s.CreateFirstUser(ctx, "admin", "longenough")
	if err != nil || first.Role != RoleAdmin {
		t.Fatalf("first user = %+v, %v; want an admin", first, err)
	}
	a, err := s.CreateUser(ctx, "bob", "longenough", RoleUser)
	if err != nil || a.Role != RoleUser || a.Disabled || a.SSO || a.Username != "bob" {
		t.Fatalf("CreateUser = %+v, %v", a, err)
	}
	sess, err := s.Login(ctx, "10.0.0.1", "bob", "longenough")
	if err != nil || sess.User.Role != RoleUser {
		t.Fatalf("Login = %+v, %v", sess.User, err)
	}
	if u, err := s.SessionUser(ctx, sess.Token); err != nil || u.Role != RoleUser {
		t.Errorf("SessionUser = %+v, %v", u, err)
	}
	_, token, err := s.CreateToken(ctx, a.ID, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if u, err := s.TokenUser(ctx, token); err != nil || u.Role != RoleUser {
		t.Errorf("TokenUser = %+v, %v; want the owner's role", u, err)
	}

	tests := []struct {
		name, user, password string
		role                 Role
		// want is nil for an InputError.
		want error
	}{
		{"taken", "BOB", "longenough", RoleUser, ErrUserExists},
		{"bad role", "carol", "longenough", "root", nil},
		{"no role", "carol", "longenough", "", nil},
		{"short password", "carol", "short", RoleUser, nil},
		{"bad name", " carol", "longenough", RoleUser, nil},
	}
	for _, tt := range tests {
		_, err := s.CreateUser(ctx, tt.user, tt.password, tt.role)
		var inputErr *InputError
		if tt.want == nil && !errors.As(err, &inputErr) || tt.want != nil && !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
	}
	users, err := s.ListUsers(ctx)
	if err != nil || len(users) != 2 || users[0].Username != "admin" || users[1].Username != "bob" {
		t.Errorf("ListUsers = %+v, %v", users, err)
	}
}

func TestUpdateUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	admin, err := s.CreateFirstUser(ctx, "admin", "longenough")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser(ctx, "bob", "longenough", RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Login(ctx, "10.0.0.1", "bob", "longenough")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.CreateToken(ctx, bob.ID, "ci")
	if err != nil {
		t.Fatal(err)
	}

	// The last enabled admin stays one.
	for _, c := range []Change{{Role: ptr(RoleUser)}, {Disabled: ptr(true)}} {
		if _, err := s.UpdateUser(ctx, admin.ID, c); !errors.Is(err, ErrLastAdmin) {
			t.Errorf("UpdateUser(last admin, %+v) err = %v", c, err)
		}
	}
	if _, err := s.UpdateUser(ctx, 999, Change{Disabled: ptr(true)}); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("unknown user err = %v", err)
	}
	var inputErr *InputError
	if _, err := s.UpdateUser(ctx, bob.ID, Change{Role: ptr(Role("root"))}); !errors.As(err, &inputErr) {
		t.Errorf("bad role err = %v", err)
	}

	// Disabling ends the sessions, stops tokens and login; only Disabled changes.
	a, err := s.UpdateUser(ctx, bob.ID, Change{Disabled: ptr(true)})
	if err != nil || !a.Disabled || a.Role != RoleUser {
		t.Fatalf("disable = %+v, %v", a, err)
	}
	if _, err := s.SessionUser(ctx, sess.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("session of a disabled user err = %v", err)
	}
	if _, err := s.TokenUser(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("token of a disabled user err = %v", err)
	}
	if _, err := s.Login(ctx, "10.0.0.2", "bob", "longenough"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("login of a disabled user err = %v", err)
	}
	if a, err = s.UpdateUser(ctx, bob.ID, Change{Role: ptr(RoleAdmin)}); err != nil || !a.Disabled || a.Role != RoleAdmin {
		t.Fatalf("promote = %+v, %v; want still disabled", a, err)
	}
	// A disabled admin doesn't count: admin is still the last enabled one.
	if _, err := s.UpdateUser(ctx, admin.ID, Change{Disabled: ptr(true)}); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("disable the last enabled admin err = %v", err)
	}
	if a, err = s.UpdateUser(ctx, bob.ID, Change{Disabled: ptr(false)}); err != nil || a.Disabled {
		t.Fatalf("enable = %+v, %v", a, err)
	}
	if _, err := s.TokenUser(ctx, token); err != nil {
		t.Errorf("token of an enabled user err = %v", err)
	}
	if _, err := s.UpdateUser(ctx, admin.ID, Change{Role: ptr(RoleUser)}); err != nil {
		t.Errorf("demote with another admin err = %v", err)
	}

	// A password reset ends the sessions; the new password works.
	sess, err = s.Login(ctx, "10.0.0.3", "bob", "longenough")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateUser(ctx, bob.ID, Change{Password: ptr("evenlonger")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, sess.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("session after a reset err = %v", err)
	}
	if _, err := s.Login(ctx, "10.0.0.3", "bob", "evenlonger"); err != nil {
		t.Errorf("login with the new password err = %v", err)
	}
}

func TestUpdateSSOUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	if _, err := s.CreateFirstUser(ctx, "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.SSOLogin(ctx, Identity{Issuer: "https://auth.example.net", Subject: "u-1", Username: "carol", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	var inputErr *InputError
	for _, c := range []Change{{Role: ptr(RoleAdmin)}, {Password: ptr("longenough")}} {
		if _, err := s.UpdateUser(ctx, sess.User.ID, c); !errors.As(err, &inputErr) {
			t.Errorf("UpdateUser(sso, %+v) err = %v", c, err)
		}
	}
	// Its current role is no change.
	if a, err := s.UpdateUser(ctx, sess.User.ID, Change{Role: ptr(RoleUser), Disabled: ptr(true)}); err != nil || !a.Disabled || !a.SSO {
		t.Errorf("disable sso user = %+v, %v", a, err)
	}
}

func TestDeleteUser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	admin, err := s.CreateFirstUser(ctx, "admin", "longenough")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser(ctx, "bob", "longenough", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Login(ctx, "10.0.0.1", "bob", "longenough")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.CreateToken(ctx, bob.ID, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, sess.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("deleted user's session err = %v", err)
	}
	if _, err := s.TokenUser(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("deleted user's token err = %v", err)
	}
	if err := s.DeleteUser(ctx, bob.ID); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("delete again err = %v", err)
	}
	if err := s.DeleteUser(ctx, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("delete the last admin err = %v", err)
	}
}

// TestLastAdminConcurrent demotes and deletes two admins at once: one change must fail.
func TestLastAdminConcurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for range 10 {
		s, _ := newTestService(t)
		a, err := s.CreateFirstUser(ctx, "admin", "longenough")
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateUser(ctx, "bob", "longenough", RoleAdmin)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = s.UpdateUser(ctx, a.ID, Change{Role: ptr(RoleUser)}) })
		wg.Go(func() { _ = s.DeleteUser(ctx, b.ID) })
		wg.Wait()
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND NOT disabled`).Scan(&n); err != nil || n < 1 {
			t.Fatalf("enabled admins = %d, %v; want at least 1", n, err)
		}
	}
}

// TestBreakGlassAdmin keeps the local admin while password login is off for everyone else.
func TestBreakGlassAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, passwordOff := range []bool{true, false} {
		s, _ := newSSOService(t, Options{AdminPasswordOnly: passwordOff})
		admin, err := s.CreateFirstUser(ctx, AdminUsername, "longenough")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateUser(ctx, "bob", "longenough", RoleAdmin); err != nil {
			t.Fatal(err)
		}
		_, demote := s.UpdateUser(ctx, admin.ID, Change{Role: ptr(RoleUser)})
		_, disable := s.UpdateUser(ctx, admin.ID, Change{Disabled: ptr(true), Role: ptr(RoleAdmin)})
		del := s.DeleteUser(ctx, admin.ID)
		for name, err := range map[string]error{"demote": demote, "disable": disable, "delete": del} {
			if got := errors.Is(err, ErrBreakGlass); got != passwordOff {
				t.Errorf("password off %v: %s err = %v", passwordOff, name, err)
			}
		}
		if passwordOff {
			if _, err := s.UpdateUser(ctx, admin.ID, Change{Password: ptr("evenlonger")}); err != nil {
				t.Errorf("password reset of the break-glass admin err = %v", err)
			}
		}
	}
}
