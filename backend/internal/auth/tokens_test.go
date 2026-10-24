package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTokenTestService(t *testing.T) (*Service, *fakeClock, User) {
	t.Helper()
	s, clock := newTestService(t)
	u, err := s.CreateFirstUser(context.Background(), "admin", "longenough")
	if err != nil {
		t.Fatal(err)
	}
	return s, clock, u
}

func TestCreateToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		tokenName string
		wantInput bool
	}{
		{name: "ok", tokenName: "ci"},
		{name: "unicode", tokenName: "déploiement"},
		{name: "empty", tokenName: "", wantInput: true},
		{name: "too long", tokenName: strings.Repeat("x", maxTokenName+1), wantInput: true},
		{name: "control character", tokenName: "a\nb", wantInput: true},
		{name: "surrounding space", tokenName: " ci", wantInput: true},
		{name: "invalid utf-8", tokenName: "\xff", wantInput: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, clock, u := newTokenTestService(t)
			tok, secret, err := s.CreateToken(context.Background(), u.ID, tt.tokenName)
			var inputErr *InputError
			if tt.wantInput {
				if !errors.As(err, &inputErr) {
					t.Fatalf("err = %v, want InputError", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(secret, TokenPrefix) || len(secret) < len(TokenPrefix)+26 {
				t.Errorf("secret = %q, want %s + 26 random characters", secret, TokenPrefix)
			}
			if tok.Prefix != secret[:tokenShownLen] || tok.Name != tt.tokenName || !tok.CreatedAt.Equal(clock.Now()) || tok.LastUsedAt != nil {
				t.Errorf("token = %+v", tok)
			}
			var stored int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE token_hash = ? AND instr(token_hash, ?) = 0`, tokenHash(secret), secret).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != 1 {
				t.Error("token not stored by its hash")
			}
		})
	}
}

func TestCreateTokenConflicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, u := newTokenTestService(t)
	if _, _, err := s.CreateToken(ctx, u.ID, "ci"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateToken(ctx, u.ID, "ci"); !errors.Is(err, ErrTokenExists) {
		t.Errorf("duplicate name: err = %v, want ErrTokenExists", err)
	}
	for i := 1; i < maxTokensPerUser; i++ {
		if _, _, err := s.CreateToken(ctx, u.ID, fmt.Sprintf("t%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.CreateToken(ctx, u.ID, "one more"); !errors.Is(err, ErrTooManyTokens) {
		t.Errorf("over the limit: err = %v, want ErrTooManyTokens", err)
	}
}

func TestCreateTokenParallelRespectsLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, u := newTokenTestService(t)
	for i := range maxTokensPerUser - 5 {
		if _, _, err := s.CreateToken(ctx, u.ID, fmt.Sprintf("t%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() { _, _, _ = s.CreateToken(ctx, u.ID, fmt.Sprintf("p%d", i)) })
	}
	wg.Wait()
	tokens, err := s.ListTokens(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != maxTokensPerUser {
		t.Errorf("%d tokens, want %d", len(tokens), maxTokensPerUser)
	}
}

func TestTokenLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, u := newTokenTestService(t)
	tok, secret, err := s.CreateToken(ctx, u.ID, "ci")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.TokenUser(ctx, secret)
	if err != nil || got.ID != u.ID || got.Username != "admin" || got.Token != "ci" {
		t.Fatalf("TokenUser = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "nudge_", secret + "x", strings.TrimPrefix(secret, TokenPrefix), tokenHash(secret)} {
		if _, err := s.TokenUser(ctx, bad); !errors.Is(err, ErrNoSession) {
			t.Errorf("TokenUser(%q) err = %v, want ErrNoSession", bad, err)
		}
	}
	if err := s.DeleteToken(ctx, u.ID+1, tok.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("delete by another user: err = %v, want ErrTokenNotFound", err)
	}
	if err := s.DeleteToken(ctx, u.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteToken(ctx, u.ID, tok.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("second delete: err = %v, want ErrTokenNotFound", err)
	}
	if _, err := s.TokenUser(ctx, secret); !errors.Is(err, ErrNoSession) {
		t.Errorf("revoked token: err = %v, want ErrNoSession", err)
	}
}

func TestTokenExpires(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock, u := newTokenTestService(t)
	_, secret, err := s.CreateToken(ctx, u.ID, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE api_tokens SET expires_at = ?`, clock.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TokenUser(ctx, secret); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	clock.Advance(time.Hour)
	if _, err := s.TokenUser(ctx, secret); !errors.Is(err, ErrNoSession) {
		t.Errorf("after expiry: err = %v, want ErrNoSession", err)
	}
}

func TestTokenLastUsedThrottled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, clock, u := newTokenTestService(t)
	_, secret, err := s.CreateToken(ctx, u.ID, "ci")
	if err != nil {
		t.Fatal(err)
	}
	lastUsed := func() *time.Time {
		t.Helper()
		tokens, err := s.ListTokens(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return tokens[0].LastUsedAt
	}
	if lastUsed() != nil {
		t.Fatal("last_used_at set before first use")
	}
	use := func() {
		t.Helper()
		if _, err := s.TokenUser(ctx, secret); err != nil {
			t.Fatal(err)
		}
	}
	use()
	first := clock.Now()
	if lu := lastUsed(); lu == nil || !lu.Equal(first) {
		t.Fatalf("last_used_at = %v, want %v", lu, first)
	}
	clock.Advance(30 * time.Second)
	use()
	if lu := lastUsed(); !lu.Equal(first) {
		t.Errorf("last_used_at moved within touchInterval: %v", lu)
	}
	clock.Advance(30 * time.Second)
	use()
	if lu := lastUsed(); !lu.Equal(clock.Now()) {
		t.Errorf("last_used_at = %v, want %v", lu, clock.Now())
	}
}

func TestListTokensOnlyOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, u := newTokenTestService(t)
	if tokens, err := s.ListTokens(ctx, u.ID); err != nil || tokens == nil || len(tokens) != 0 {
		t.Fatalf("empty list = %#v, %v; want empty non-nil slice", tokens, err)
	}
	if _, _, err := s.CreateToken(ctx, u.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateToken(ctx, u.ID, "b"); err != nil {
		t.Fatal(err)
	}
	tokens, err := s.ListTokens(ctx, u.ID)
	if err != nil || len(tokens) != 2 || tokens[0].Name != "a" || tokens[1].Name != "b" {
		t.Errorf("ListTokens = %+v, %v", tokens, err)
	}
	if other, err := s.ListTokens(ctx, u.ID+1); err != nil || len(other) != 0 {
		t.Errorf("other user's list = %+v, %v", other, err)
	}
}
