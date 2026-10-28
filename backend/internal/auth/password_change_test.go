package auth

import (
	"context"
	"errors"
	"testing"
)

func TestChangePassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	u, err := s.CreateFirstUser(ctx, "admin", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := s.Login(ctx, "192.0.2.1", "admin", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Login(ctx, "192.0.2.2", "admin", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	sso, err := s.SSOLogin(ctx, Identity{Issuer: "https://auth.example.net", Subject: "u-1", Username: "carol", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}

	var inputErr *InputError
	tests := []struct {
		name    string
		id      int64
		current string
		next    string
		check   func(error) bool
	}{
		{"wrong current", u.ID, "not-it-at-all", "new-password", func(err error) bool { return errors.Is(err, ErrInvalidCredentials) }},
		{"too long current", u.ID, string(make([]byte, maxPasswordLen+1)), "new-password", func(err error) bool { return errors.Is(err, ErrInvalidCredentials) }},
		{"short new", u.ID, "old-password", "short", func(err error) bool { return errors.As(err, &inputErr) }},
		{"sso user", sso.User.ID, "", "new-password", func(err error) bool { return errors.As(err, &inputErr) }},
		{"unknown user", 999, "old-password", "new-password", func(err error) bool { return errors.Is(err, ErrUserNotFound) }},
	}
	for _, tt := range tests {
		if err := s.ChangePassword(ctx, "192.0.2.1", tt.id, mine.Token, tt.current, tt.next); !tt.check(err) {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
	if _, err := s.SessionUser(ctx, other.Token); err != nil {
		t.Fatalf("a refused change ended a session: %v", err)
	}

	if err := s.ChangePassword(ctx, "192.0.2.1", u.ID, mine.Token, "old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, mine.Token); err != nil {
		t.Errorf("the session that changed the password ended: %v", err)
	}
	if _, err := s.SessionUser(ctx, other.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("another session survived: %v", err)
	}
	if _, err := s.Login(ctx, "192.0.2.3", "admin", "old-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still works: %v", err)
	}
	if _, err := s.Login(ctx, "192.0.2.3", "admin", "new-password"); err != nil {
		t.Errorf("new password: %v", err)
	}
}

func TestChangePasswordRateLimited(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	u, err := s.CreateFirstUser(ctx, "admin", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	// Wrong guesses from many addresses still hit the user's limit, as logins do.
	var limited *RateLimitedError
	for i := range userFailureLimit + 1 {
		err = s.ChangePassword(ctx, "192.0.2."+string(rune('1'+i%9)), u.ID, "", "wrong-guess", "new-password")
		if errors.As(err, &limited) {
			break
		}
	}
	if !errors.As(err, &limited) {
		t.Fatalf("after %d wrong guesses: %v", userFailureLimit+1, err)
	}
	if _, err := s.Login(ctx, "198.51.100.1", "admin", "old-password"); !errors.As(err, &limited) {
		t.Errorf("login after the guesses: %v", err)
	}
}
