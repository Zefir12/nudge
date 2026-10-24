package environments

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
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

// newTestService returns a Service whose environments all get fake (one per Connect
// call; the last one is returned by the func).
func newTestService(t *testing.T, fake *dockertest.Fake) (*Service, *fakeClock, func() int) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	var mu sync.Mutex
	connects := 0
	s := New(Options{DB: st.DB(), Now: clock.Now, Connect: func(Environment) (docker.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		connects++
		return fake, nil
	}})
	return s, clock, func() int {
		mu.Lock()
		defer mu.Unlock()
		return connects
	}
}

func TestEnsureLocal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("unreachable host creates nothing", func(t *testing.T) {
		t.Parallel()
		fake := &dockertest.Fake{PingErr: errors.New("connection refused")}
		s, _, _ := newTestService(t, fake)
		created, err := s.EnsureLocal(ctx)
		if err != nil || created {
			t.Fatalf("EnsureLocal = %v, %v; want false, nil", created, err)
		}
		if envs, _ := s.List(ctx); len(envs) != 0 {
			t.Errorf("environments = %+v, want none", envs)
		}
		if !fake.Closed() {
			t.Error("probe client not closed")
		}
	})

	t.Run("reachable host creates local once", func(t *testing.T) {
		t.Parallel()
		s, clock, _ := newTestService(t, &dockertest.Fake{})
		for i, want := range []bool{true, false} {
			created, err := s.EnsureLocal(ctx)
			if err != nil || created != want {
				t.Fatalf("call %d: EnsureLocal = %v, %v; want %v", i, created, err, want)
			}
		}
		envs, err := s.List(ctx)
		if err != nil || len(envs) != 1 {
			t.Fatalf("List = %+v, %v", envs, err)
		}
		env := envs[0]
		if env.Name != LocalName || env.Kind != KindLocal || !env.CreatedAt.Equal(clock.Now()) || env.LastSeenAt == nil {
			t.Errorf("env = %+v", env)
		}
	})

	t.Run("an environment named local blocks it", func(t *testing.T) {
		t.Parallel()
		s, _, _ := newTestService(t, &dockertest.Fake{})
		if _, err := s.db.Exec(`INSERT INTO environments (name, kind, created_at) VALUES ('local', 'agent', 0)`); err != nil {
			t.Fatal(err)
		}
		created, err := s.EnsureLocal(ctx)
		if err != nil || created {
			t.Fatalf("EnsureLocal = %v, %v; want false, nil", created, err)
		}
	})
}

func TestGetAndClient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &dockertest.Fake{}
	s, _, connects := newTestService(t, fake)
	if _, err := s.EnsureLocal(ctx); err != nil {
		t.Fatal(err)
	}
	probes := connects()
	envs, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	env, err := s.Get(ctx, envs[0].ID)
	if err != nil || env.ID != envs[0].ID || env.Name != envs[0].Name {
		t.Fatalf("Get = %+v, %v", env, err)
	}
	if _, err := s.Get(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(999) err = %v, want ErrNotFound", err)
	}
	for range 3 {
		if _, err := s.Client(env); err != nil {
			t.Fatal(err)
		}
	}
	if got := connects() - probes; got != 1 {
		t.Errorf("%d connects for 3 Client calls, want 1 (cached)", got)
	}
	if err := s.Close(); err != nil || !fake.Closed() {
		t.Errorf("Close = %v, closed %v", err, fake.Closed())
	}
}

func TestOnline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &dockertest.Fake{}
	s, clock, _ := newTestService(t, fake)
	if _, err := s.EnsureLocal(ctx); err != nil {
		t.Fatal(err)
	}
	stored := func() time.Time {
		t.Helper()
		envs, err := s.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return *envs[0].LastSeenAt
	}
	envs, _ := s.List(ctx)
	env := envs[0]
	created := stored()

	clock.Advance(30 * time.Second)
	if !s.Online(ctx, &env) {
		t.Fatal("Online = false")
	}
	if got := stored(); !got.Equal(created) {
		t.Errorf("last_seen_at moved within touchInterval: %v", got)
	}
	clock.Advance(30 * time.Second)
	if !s.Online(ctx, &env) {
		t.Fatal("Online = false")
	}
	if got := stored(); !got.Equal(clock.Now()) || !env.LastSeenAt.Equal(clock.Now()) {
		t.Errorf("last_seen_at = %v (env %v), want %v", got, env.LastSeenAt, clock.Now())
	}

	offline := &dockertest.Fake{PingErr: errors.New("connection refused")}
	s2, _, _ := newTestService(t, offline)
	if _, err := s2.db.Exec(`INSERT INTO environments (name, kind, created_at) VALUES ('local', 'local', 0)`); err != nil {
		t.Fatal(err)
	}
	envs2, _ := s2.List(ctx)
	if s2.Online(ctx, &envs2[0]) {
		t.Error("Online = true for a failing ping")
	}
	if envs2[0].LastSeenAt != nil {
		t.Error("last_seen_at set for an offline environment")
	}
}

func TestLocalConnect(t *testing.T) {
	t.Parallel()
	connect, err := LocalConnect("unix:///nonexistent/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	c, err := connect(Environment{ID: 1, Kind: KindLocal})
	if err != nil {
		t.Fatalf("local: %v", err)
	}
	_ = c.Close()
	if _, err := connect(Environment{ID: 2, Kind: KindAgent}); err == nil {
		t.Error("agent kind connected, want an error until phase 2")
	}
	if _, err := LocalConnect("ssh://host"); err == nil {
		t.Error("ssh host accepted")
	}
}
