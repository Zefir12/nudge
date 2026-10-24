package updates

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	"github.com/opencontainers/go-digest"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

var start = time.Unix(1_700_000_000, 0).UTC()

// fakeClock stands still; After records the wait and fires at once.
type fakeClock struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (c *fakeClock) Now() time.Time { return start }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- start
	return ch
}

func (c *fakeClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.waits)
}

type fakeEnvs struct {
	envs    []environments.Environment
	clients map[int64]docker.Client
}

func (f *fakeEnvs) List(context.Context) ([]environments.Environment, error) { return f.envs, nil }

func (f *fakeEnvs) Client(env environments.Environment) (docker.Client, error) {
	return f.clients[env.ID], nil
}

// newTestService returns a Service over a migrated database with environments 1 and 2,
// each with its own fake Docker client.
func newTestService(t *testing.T, auth func(ctx context.Context, ref string) (registries.Auth, bool, error)) (*Service, *fakeEnvs, *fakeClock) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	envs := &fakeEnvs{clients: map[int64]docker.Client{}}
	for _, id := range []int64{1, 2} {
		if _, err := st.DB().Exec(`INSERT INTO environments (id, name, kind, created_at) VALUES (?, ?, 'agent', 0)`, id, "env"+string(rune('0'+id))); err != nil {
			t.Fatal(err)
		}
		envs.envs = append(envs.envs, environments.Environment{ID: id})
		envs.clients[id] = &dockertest.Fake{}
	}
	clock := &fakeClock{}
	return New(Options{DB: st.DB(), Envs: envs, Auth: auth, Clock: clock}), envs, clock
}

func ctr(ref, id string) container.Summary { return container.Summary{Image: ref, ImageID: id} }

func pulled(digests ...string) image.InspectResponse {
	return image.InspectResponse{RepoDigests: digests}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	s, envs, _ := newTestService(t, nil)
	fake := envs.clients[1].(*dockertest.Fake)
	fake.Containers = []container.Summary{
		ctr("nginx:1.27", "sha256:n1"),
		ctr("nginx:1.27", "sha256:n1"), // a second container, the same image
		ctr("redis", "sha256:r1"),
		ctr("docker.io/library/postgres:16", "sha256:p1"),
		ctr("ghcr.io/o/app:v1", "sha256:a1"),
		ctr("myapp:dev", "sha256:m1"),
		ctr("nginx@sha256:0123456789abcdef0123456789abcdef", "sha256:n1"),
		ctr("sha256:0123456789abcdef0123", "sha256:0123456789abcdef0123"),
		ctr("0123456789ab", "sha256:0123456789ab"),
	}
	fake.ImageDetails = map[string]image.InspectResponse{
		"sha256:n1": pulled("nginx@sha256:aaa"),
		"sha256:r1": pulled("redis@sha256:old"),
		"sha256:p1": pulled("other@sha256:zzz", "postgres@sha256:ppp"),
		"sha256:a1": pulled("ghcr.io/o/app@sha256:app"),
		"sha256:m1": pulled(),
	}
	fake.Distribution = map[string]digest.Digest{
		"nginx:1.27":                    "sha256:aaa",
		"redis":                         "sha256:new",
		"docker.io/library/postgres:16": "sha256:ppp",
	}

	got, err := s.Check(context.Background(), envs.envs[0])
	if err != nil {
		t.Fatal(err)
	}
	want := []Result{
		{Image: "nginx:1.27", ImageID: "sha256:n1", Status: Current, LocalDigest: "sha256:aaa", RemoteDigest: "sha256:aaa", CheckedAt: start},
		{Image: "redis", ImageID: "sha256:r1", Status: Available, LocalDigest: "sha256:old", RemoteDigest: "sha256:new", CheckedAt: start},
		{Image: "docker.io/library/postgres:16", ImageID: "sha256:p1", Status: Current, LocalDigest: "sha256:ppp", RemoteDigest: "sha256:ppp", CheckedAt: start},
		{Image: "ghcr.io/o/app:v1", ImageID: "sha256:a1", Status: Unknown, LocalDigest: "sha256:app", Error: "the registry has no such tag", CheckedAt: start},
		{Image: "myapp:dev", ImageID: "sha256:m1", Status: Unknown, Error: "the image has no registry digest (built locally?)", CheckedAt: start},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Check =\n%+v\nwant\n%+v", got, want)
	}
	// The local image isn't looked up in the registry.
	for _, c := range fake.DistributionCalls() {
		if c.Ref == "myapp:dev" {
			t.Error("myapp:dev looked up in the registry")
		}
	}

	listed, err := s.List(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(want, func(a, b Result) int { return cmpString(a.Image, b.Image) })
	if !slices.Equal(listed, want) {
		t.Fatalf("List =\n%+v\nwant\n%+v", listed, want)
	}

	// The next check replaces the results: gone containers drop out.
	fake.Containers = fake.Containers[:1]
	if _, err := s.Check(context.Background(), envs.envs[0]); err != nil {
		t.Fatal(err)
	}
	listed, err = s.List(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Image != "nginx:1.27" {
		t.Fatalf("List after second check = %+v, want nginx only", listed)
	}
	if other, err := s.List(context.Background(), 2); err != nil || len(other) != 0 {
		t.Fatalf("List(2) = %+v, %v; want none", other, err)
	}
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func TestCheckRegistryErrors(t *testing.T) {
	t.Parallel()
	const password = "s3cret-pass"
	tests := []struct {
		name    string
		err     error
		authErr error
		want    string
	}{
		{"unauthorized", cerrdefs.ErrUnauthenticated, nil, "the registry refused access (check its credentials under Registries)"},
		{"forbidden", cerrdefs.ErrPermissionDenied, nil, "the registry refused access (check its credentials under Registries)"},
		{"timeout", context.DeadlineExceeded, nil, "the registry didn't answer in time"},
		{"other, with the password", errors.New("bad things for user:" + password), nil, "the registry lookup failed"},
		{"credentials", nil, errors.New("db gone"), "the registry credentials couldn't be read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			auth := func(context.Context, string) (registries.Auth, bool, error) {
				return registries.Auth{Address: "ghcr.io", Username: "u", Password: password}, tt.authErr == nil, tt.authErr
			}
			s, envs, _ := newTestService(t, auth)
			fake := envs.clients[1].(*dockertest.Fake)
			fake.Containers = []container.Summary{ctr("ghcr.io/o/app:v1", "sha256:a1")}
			fake.ImageDetails = map[string]image.InspectResponse{"sha256:a1": pulled("ghcr.io/o/app@sha256:app")}
			fake.DistributionErr = tt.err
			got, err := s.Check(context.Background(), envs.envs[0])
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Status != Unknown || got[0].Error != tt.want {
				t.Fatalf("Check = %+v, want unknown with %q", got, tt.want)
			}
		})
	}
}

func TestCheckSendsCredentials(t *testing.T) {
	t.Parallel()
	var asked []string
	var mu sync.Mutex
	auth := func(_ context.Context, ref string) (registries.Auth, bool, error) {
		mu.Lock()
		asked = append(asked, ref)
		mu.Unlock()
		if registries.HostOf(ref) != "ghcr.io" {
			return registries.Auth{}, false, nil
		}
		return registries.Auth{Address: "ghcr.io", Username: "bot", Password: "pw"}, true, nil
	}
	s, envs, _ := newTestService(t, auth)
	fake := envs.clients[1].(*dockertest.Fake)
	fake.Containers = []container.Summary{ctr("ghcr.io/o/app:v1", "sha256:a1"), ctr("nginx", "sha256:n1")}
	fake.ImageDetails = map[string]image.InspectResponse{
		"sha256:a1": pulled("ghcr.io/o/app@sha256:app"),
		"sha256:n1": pulled("nginx@sha256:aaa"),
	}
	fake.Distribution = map[string]digest.Digest{"ghcr.io/o/app:v1": "sha256:app", "nginx": "sha256:aaa"}
	if _, err := s.Check(context.Background(), envs.envs[0]); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.DistributionCalls() {
		switch c.Ref {
		case "ghcr.io/o/app:v1":
			b, err := base64.URLEncoding.DecodeString(c.Auth)
			if err != nil {
				t.Fatal(err)
			}
			var a registry.AuthConfig
			if err := json.Unmarshal(b, &a); err != nil {
				t.Fatal(err)
			}
			if a.Username != "bot" || a.Password != "pw" || a.ServerAddress != "ghcr.io" {
				t.Errorf("auth for %s = %+v", c.Ref, a)
			}
		case "nginx":
			if c.Auth != "" {
				t.Errorf("nginx sent with credentials %q", c.Auth)
			}
		}
	}
}

func TestCheckRunning(t *testing.T) {
	t.Parallel()
	s, envs, _ := newTestService(t, nil)
	if !s.lock() {
		t.Fatal("lock failed")
	}
	if _, err := s.Check(context.Background(), envs.envs[0]); !errors.Is(err, ErrRunning) {
		t.Errorf("Check while running: err = %v, want ErrRunning", err)
	}
	if err := s.CheckAll(context.Background()); !errors.Is(err, ErrRunning) {
		t.Errorf("CheckAll while running: err = %v, want ErrRunning", err)
	}
	s.unlock()
	if _, err := s.Check(context.Background(), envs.envs[0]); err != nil {
		t.Errorf("Check after: %v", err)
	}
}

func TestCheckAll(t *testing.T) {
	t.Parallel()
	s, envs, _ := newTestService(t, nil)
	for _, id := range []int64{1, 2} {
		f := envs.clients[id].(*dockertest.Fake)
		f.Containers = []container.Summary{ctr("nginx:1.27", "sha256:n1")}
		f.ImageDetails = map[string]image.InspectResponse{"sha256:n1": pulled("nginx@sha256:old")}
		f.Distribution = map[string]digest.Digest{"nginx:1.27": "sha256:new"}
	}
	if err := s.CheckAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Each environment's daemon looks the reference up itself.
	for _, id := range []int64{1, 2} {
		if calls := envs.clients[id].(*dockertest.Fake).DistributionCalls(); len(calls) != 1 {
			t.Errorf("environment %d: registry lookups = %v, want 1", id, calls)
		}
	}
	for _, id := range []int64{1, 2} {
		got, err := s.List(context.Background(), id)
		if err != nil || len(got) != 1 || got[0].Status != Available {
			t.Errorf("List(%d) = %+v, %v; want nginx available", id, got, err)
		}
	}

	// An offline environment keeps its last results; the others are still checked.
	envs.clients[1].(*dockertest.Fake).ResourceErr = &net.OpError{Op: "dial", Err: errors.New("refused")}
	f2 := envs.clients[2].(*dockertest.Fake)
	f2.Distribution = map[string]digest.Digest{"nginx:1.27": "sha256:old"}
	if err := s.CheckAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.List(context.Background(), 1); len(got) != 1 || got[0].Status != Available {
		t.Errorf("offline environment's results = %+v, want the old ones", got)
	}
	if got, _ := s.List(context.Background(), 2); len(got) != 1 || got[0].Status != Current {
		t.Errorf("environment 2 = %+v, want nginx current", got)
	}
}

func TestLimiter(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{}
	l := newLimiter(clock, RegistryGap)
	for _, host := range []string{"docker.io", "docker.io", "ghcr.io", "docker.io"} {
		if err := l.wait(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	// The clock stands still: the first request to each host goes at once, the next ones
	// are spaced by the gap.
	want := []time.Duration{RegistryGap, 2 * RegistryGap}
	if got := clock.Waits(); !slices.Equal(got, want) {
		t.Errorf("waits = %v, want %v", got, want)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	blocked := newLimiter(blockingClock{}, RegistryGap)
	_ = blocked.wait(ctx, "docker.io")
	if err := blocked.wait(ctx, "docker.io"); !errors.Is(err, context.Canceled) {
		t.Errorf("wait with a cancelled context = %v, want context.Canceled", err)
	}
}

// blockingClock never fires.
type blockingClock struct{}

func (blockingClock) Now() time.Time                       { return start }
func (blockingClock) After(time.Duration) <-chan time.Time { return nil }

func TestRun(t *testing.T) {
	t.Parallel()
	s, envs, clock := newTestService(t, nil)
	f := envs.clients[1].(*dockertest.Fake)
	f.Containers = []container.Summary{ctr("nginx", "sha256:n1")}
	f.ImageDetails = map[string]image.InspectResponse{"sha256:n1": pulled("nginx@sha256:a")}
	f.Distribution = map[string]digest.Digest{"nginx": "sha256:a"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx, time.Minute, time.Hour)
	}()
	deadline := time.After(5 * time.Second)
	for len(clock.Waits()) < 3 {
		select {
		case <-deadline:
			t.Fatal("Run didn't loop")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	waits := clock.Waits()
	if waits[0] != time.Minute || waits[1] != time.Hour {
		t.Errorf("waits = %v, want the delay, then the interval", waits[:2])
	}
	if got, _ := s.List(context.Background(), 1); len(got) != 1 {
		t.Errorf("results after Run = %+v, want nginx", got)
	}
}

func TestRepoOf(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"nginx":                           "nginx",
		"nginx:1.27":                      "nginx",
		"library/nginx:1.27":              "nginx",
		"docker.io/library/nginx:latest":  "nginx",
		"index.docker.io/library/nginx":   "nginx",
		"docker.io/grafana/grafana:11":    "grafana/grafana",
		"ghcr.io/o/app:v1":                "ghcr.io/o/app",
		"ghcr.io/library/app":             "ghcr.io/library/app",
		"localhost:5000/app:dev":          "localhost:5000/app",
		"localhost:5000/app":              "localhost:5000/app",
		"nginx@sha256:aaa":                "nginx",
		"registry.example.com:443/a/b:c1": "registry.example.com:443/a/b",
	}
	for in, want := range tests {
		if got := repoOf(in); got != want {
			t.Errorf("repoOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestByTag(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"nginx":                  true,
		"nginx:1.27":             true,
		"localhost:5000/app:dev": true,
		"nginx@sha256:abc":       false,
		"sha256:0123456789ab":    false,
		"0123456789ab":           false,
		"":                       false,
	}
	for in, want := range tests {
		if got := byTag(in); got != want {
			t.Errorf("byTag(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCheckFailures(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*Service, environments.Environment) {
		s, envs, _ := newTestService(t, nil)
		f := envs.clients[1].(*dockertest.Fake)
		f.Containers = []container.Summary{ctr("nginx", "sha256:n1")}
		f.ImageDetails = map[string]image.InspectResponse{"sha256:n1": pulled("nginx@sha256:a")}
		f.Distribution = map[string]digest.Digest{"nginx": "sha256:a"}
		return s, envs.envs[0]
	}

	t.Run("store", func(t *testing.T) {
		t.Parallel()
		s, env := setup(t)
		if err := s.db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Check(context.Background(), env); !errors.Is(err, ErrStore) {
			t.Errorf("Check with the database closed: err = %v, want ErrStore", err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		s, env := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := s.Check(ctx, env); !errors.Is(err, context.Canceled) {
			t.Errorf("Check with a cancelled context: err = %v, want context.Canceled", err)
		}
		if got, _ := s.List(context.Background(), env.ID); len(got) != 0 {
			t.Errorf("a cancelled check saved %+v", got)
		}
	})
}
