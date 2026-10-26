package environments

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func TestCreateAgent(t *testing.T) {
	t.Parallel()
	s, clock, _ := newTestService(t, &dockertest.Fake{})
	ctx := context.Background()
	env, jt, err := s.CreateAgent(ctx, "nas")
	if err != nil {
		t.Fatal(err)
	}
	if env.Kind != KindAgent || env.Name != "nas" || env.ID == 0 {
		t.Errorf("env = %+v", env)
	}
	if !strings.HasPrefix(jt.Token, JoinTokenPrefix) || len(jt.Token) != len(JoinTokenPrefix)+64 {
		t.Errorf("join token %q: want the prefix and 32 random bytes in hex", jt.Token)
	}
	if want := clock.Now().Add(24 * time.Hour); !jt.ExpiresAt.Equal(want) {
		t.Errorf("expires at %v, want %v", jt.ExpiresAt, want)
	}
	var stored string
	if err := s.db.QueryRowContext(ctx, `SELECT token_hash FROM join_tokens WHERE environment_id = ?`, env.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == jt.Token || stored != hashSecret(jt.Token) {
		t.Errorf("stored %q, want the token's hash", stored)
	}
	got, err := s.Get(ctx, env.ID)
	if err != nil || got.Enrolled || got.Kind != KindAgent {
		t.Errorf("Get = %+v, %v; want a not yet enrolled agent", got, err)
	}

	tests := []struct {
		name string
		in   string
		want error
	}{
		{name: "taken", in: "nas", want: ErrNameTaken},
		{name: "empty", in: ""},
		{name: "too long", in: strings.Repeat("a", 65)},
		{name: "control character", in: "a\nb"},
		{name: "surrounding space", in: " nas2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := s.CreateAgent(ctx, tt.in)
			var inputErr *InputError
			if tt.want != nil && !errors.Is(err, tt.want) || tt.want == nil && !errors.As(err, &inputErr) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestEnroll(t *testing.T) {
	t.Parallel()
	s, clock, _ := newTestService(t, &dockertest.Fake{})
	ctx := context.Background()
	env, jt, err := s.CreateAgent(ctx, "nas")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := s.Enroll(ctx, jt.Token)
	if err != nil {
		t.Fatal(err)
	}
	if creds.EnvironmentID != env.ID || !strings.HasPrefix(creds.AgentID, agentIDPrefix) || !strings.HasPrefix(creds.Secret, AgentSecretPrefix) || creds.Replaced {
		t.Errorf("creds = %+v", creds)
	}
	var stored string
	if err := s.db.QueryRowContext(ctx, `SELECT agent_secret_hash FROM environments WHERE id = ?`, env.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != hashSecret(creds.Secret) {
		t.Errorf("stored secret %q, want its hash", stored)
	}
	if got, err := s.AuthenticateAgent(ctx, creds.AgentID, creds.Secret); err != nil || got.ID != env.ID || !got.Enrolled {
		t.Errorf("AuthenticateAgent = %+v, %v", got, err)
	}
	if _, err := s.Enroll(ctx, jt.Token); !errors.Is(err, ErrInvalidJoinToken) {
		t.Errorf("second use: err = %v, want ErrInvalidJoinToken", err)
	}

	// A new token re-enrolls: the old credentials stop working.
	jt2, err := s.RegenerateJoinToken(ctx, env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateAgent(ctx, creds.AgentID, creds.Secret); err != nil {
		t.Errorf("credentials must stay valid until the new token is used: %v", err)
	}
	creds2, err := s.Enroll(ctx, jt2.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !creds2.Replaced {
		t.Error("re-enroll: Replaced = false, want true")
	}
	if _, err := s.AuthenticateAgent(ctx, creds.AgentID, creds.Secret); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old credentials: err = %v, want ErrInvalidCredentials", err)
	}

	// Expiry follows the clock.
	jt3, err := s.RegenerateJoinToken(ctx, env.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(JoinTokenTTL)
	if _, err := s.Enroll(ctx, jt3.Token); !errors.Is(err, ErrInvalidJoinToken) {
		t.Errorf("expired token: err = %v, want ErrInvalidJoinToken", err)
	}
	if _, err := s.AuthenticateAgent(ctx, creds2.AgentID, creds2.Secret); err != nil {
		t.Errorf("a failed enroll must not touch the credentials: %v", err)
	}

	for _, token := range []string{"", "nudge_join_nope", jt.Token[:len(jt.Token)-1], "x" + jt3.Token} {
		if _, err := s.Enroll(ctx, token); !errors.Is(err, ErrInvalidJoinToken) {
			t.Errorf("Enroll(%q) = %v, want ErrInvalidJoinToken", token, err)
		}
	}
}

func TestEnrollOnce(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t, &dockertest.Fake{})
	_, jt, err := s.CreateAgent(context.Background(), "nas")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := s.Enroll(context.Background(), jt.Token)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrInvalidJoinToken):
			t.Errorf("err = %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d enrolls succeeded, want 1", ok)
	}
}

func TestAuthenticateAgent(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t, &dockertest.Fake{})
	ctx := context.Background()
	_, jt, err := s.CreateAgent(ctx, "nas")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := s.Enroll(ctx, jt.Token)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, id, secret string
	}{
		{name: "wrong secret", id: creds.AgentID, secret: creds.Secret + "x"},
		{name: "unknown agent", id: "agt_nope", secret: creds.Secret},
		{name: "empty", id: "", secret: ""},
	}
	for _, tt := range tests {
		if _, err := s.AuthenticateAgent(ctx, tt.id, tt.secret); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: err = %v, want ErrInvalidCredentials", tt.name, err)
		}
	}
}

func TestRevoke(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t, &dockertest.Fake{})
	ctx := context.Background()
	env, jt, err := s.CreateAgent(ctx, "nas")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := s.Enroll(ctx, jt.Token)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.RegenerateJoinToken(ctx, env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, env.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateAgent(ctx, creds.AgentID, creds.Secret); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("after revoke: err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := s.Enroll(ctx, pending.Token); !errors.Is(err, ErrInvalidJoinToken) {
		t.Errorf("pending token after revoke: err = %v, want ErrInvalidJoinToken", err)
	}
	if got, err := s.Get(ctx, env.ID); err != nil || got.Enrolled {
		t.Errorf("Get = %+v, %v; want the environment kept, not enrolled", got, err)
	}
}

func TestAgentCallsNeedAgentEnvironment(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t, &dockertest.Fake{})
	ctx := context.Background()
	if _, err := s.EnsureLocal(ctx); err != nil {
		t.Fatal(err)
	}
	envs, err := s.List(ctx)
	if err != nil || len(envs) != 1 {
		t.Fatalf("List = %v, %v", envs, err)
	}
	local := envs[0].ID
	if _, err := s.RegenerateJoinToken(ctx, local); !errors.Is(err, ErrNotAgent) {
		t.Errorf("RegenerateJoinToken(local) = %v, want ErrNotAgent", err)
	}
	if err := s.Revoke(ctx, local); !errors.Is(err, ErrNotAgent) {
		t.Errorf("Revoke(local) = %v, want ErrNotAgent", err)
	}
	if _, err := s.RegenerateJoinToken(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("RegenerateJoinToken(999) = %v, want ErrNotFound", err)
	}
	if err := s.Revoke(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Revoke(999) = %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	s, clock, _ := newTestService(t, &dockertest.Fake{})
	ctx := context.Background()
	if _, err := s.EnsureLocal(ctx); err != nil {
		t.Fatal(err)
	}
	env, jt, err := s.CreateAgent(ctx, "nas")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := s.Enroll(ctx, jt.Token)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.RegenerateJoinToken(ctx, env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Client(env); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, env.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, env.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete: err = %v, want ErrNotFound", err)
	}
	if _, err := s.AuthenticateAgent(ctx, creds.AgentID, creds.Secret); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("credentials after delete: err = %v, want ErrInvalidCredentials", err)
	}
	if _, err := s.Enroll(ctx, pending.Token); !errors.Is(err, ErrInvalidJoinToken) {
		t.Errorf("join token after delete: err = %v, want ErrInvalidJoinToken", err)
	}
	var tokens int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM join_tokens`).Scan(&tokens); err != nil || tokens != 0 {
		t.Errorf("join_tokens rows = %d, %v; want 0", tokens, err)
	}
	s.mu.Lock()
	_, cached := s.clients[env.ID]
	s.mu.Unlock()
	if cached {
		t.Error("the removed environment's client is still cached")
	}
	if err := s.Delete(ctx, env.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}

	// A request that still held the removed environment gets a client; a new environment
	// that reuses the id must not be served that one.
	if _, err := s.Client(env); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	reused, _, err := s.CreateAgent(ctx, "nas2")
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != env.ID {
		t.Fatalf("new environment id = %d, want the reused %d", reused.ID, env.ID)
	}
	if _, err := s.Client(reused); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	got := s.clients[reused.ID].created
	s.mu.Unlock()
	if !got.Equal(reused.CreatedAt) {
		t.Errorf("cached client is for an environment created at %v, want %v", got, reused.CreatedAt)
	}
	if err := s.Delete(ctx, reused.ID); err != nil {
		t.Fatal(err)
	}
	envs, err := s.List(ctx)
	if err != nil || len(envs) != 1 {
		t.Fatalf("List = %v, %v", envs, err)
	}
	if err := s.Delete(ctx, envs[0].ID); !errors.Is(err, ErrLocal) {
		t.Errorf("delete local: err = %v, want ErrLocal", err)
	}
}
