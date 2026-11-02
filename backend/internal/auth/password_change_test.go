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
	mine, err := s.Login(ctx, "admin", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Login(ctx, "admin", "old-password")
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
		if err := s.ChangePassword(ctx, tt.id, mine.Token, tt.current, tt.next); !tt.check(err) {
			t.Errorf("%s: err = %v", tt.name, err)
		}
	}
	if _, err := s.SessionUser(ctx, other.Token); err != nil {
		t.Fatalf("a refused change ended a session: %v", err)
	}

	if err := s.ChangePassword(ctx, u.ID, mine.Token, "old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, mine.Token); err != nil {
		t.Errorf("the session that changed the password ended: %v", err)
	}
	if _, err := s.SessionUser(ctx, other.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("another session survived: %v", err)
	}
	if _, err := s.Login(ctx, "admin", "old-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still works: %v", err)
	}
	if _, err := s.Login(ctx, "admin", "new-password"); err != nil {
		t.Errorf("new password: %v", err)
	}
}

func TestChangePasswordWrongGuessesBlockNobody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestService(t)
	u, err := s.CreateFirstUser(ctx, "admin", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		if err := s.ChangePassword(ctx, u.ID, "", "wrong-guess", "new-password"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	if _, err := s.Login(ctx, "admin", "old-password"); err != nil {
		t.Errorf("login after the guesses: %v", err)
	}
	if err := s.ChangePassword(ctx, u.ID, "", "old-password", "new-password"); err != nil {
		t.Errorf("right password after the guesses: %v", err)
	}
}
