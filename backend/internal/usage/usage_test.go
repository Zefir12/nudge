package usage

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/moby/moby/api/types/volume"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

// fakeEnvs serves one fake per environment, by name.
type fakeEnvs struct {
	list  []environments.Environment
	fakes map[string]*dockertest.Fake
}

func (f *fakeEnvs) List(context.Context) ([]environments.Environment, error) { return f.list, nil }

func (f *fakeEnvs) Client(env environments.Environment) (docker.Client, error) {
	return f.fakes[env.Name], nil
}

// fakeClock fires After when the test sends on after.
type fakeClock struct {
	now   time.Time
	after chan time.Time
	waits chan time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.waits <- d
	return c.after
}

type usageTest struct {
	s     *Service
	envs  *fakeEnvs
	clock *fakeClock
}

// newUsageTest has one environment per fake, named after its key.
func newUsageTest(t *testing.T, fakes map[string]*dockertest.Fake) usageTest {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	envs := &fakeEnvs{fakes: fakes}
	for name := range fakes {
		res, err := st.DB().ExecContext(ctx, `INSERT INTO environments (name, kind, created_at) VALUES (?, 'local', 0)`, name)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		envs.list = append(envs.list, environments.Environment{ID: id, Name: name, Kind: environments.KindLocal})
	}
	clock := &fakeClock{now: time.UnixMilli(1_700_000_000_000), after: make(chan time.Time), waits: make(chan time.Duration, 1)}
	return usageTest{s: New(Options{DB: st.DB(), Envs: envs, Clock: clock}), envs: envs, clock: clock}
}

func (ut usageTest) env(name string) environments.Environment {
	for _, e := range ut.envs.list {
		if e.Name == name {
			return e
		}
	}
	panic("no environment " + name)
}

func ptr(n int64) *int64 { return &n }

func TestMeasure(t *testing.T) {
	t.Parallel()
	fake := &dockertest.Fake{Resources: dockertest.Resources{
		Volumes: []volume.Volume{{Name: "db"}, {Name: "nfs"}, {Name: "new"}},
		VolumeUsage: map[string]volume.UsageData{
			"db":  {Size: 4096, RefCount: 1},
			"nfs": {Size: -1, RefCount: 0},
		},
	}}
	ut := newUsageTest(t, map[string]*dockertest.Fake{"local": fake})
	ctx := context.Background()
	env := ut.env("local")
	if err := ut.s.Measure(ctx, env); err != nil {
		t.Fatal(err)
	}
	got, err := ut.s.Volumes(ctx, env.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := ut.clock.now
	want := map[string]Volume{
		"db":  {Size: ptr(4096), RefCount: 1, MeasuredAt: at},
		"nfs": {RefCount: 0, MeasuredAt: at},
		"new": {RefCount: -1, MeasuredAt: at},
	}
	if len(got) != len(want) {
		t.Fatalf("Volumes = %+v, want %+v", got, want)
	}
	for name, w := range want {
		g := got[name]
		if (g.Size == nil) != (w.Size == nil) || (g.Size != nil && *g.Size != *w.Size) || g.RefCount != w.RefCount || !g.MeasuredAt.Equal(w.MeasuredAt) {
			t.Errorf("%s = %+v, want %+v", name, g, w)
		}
	}

	// A removed volume goes with the next measurement.
	fake.Volumes = fake.Volumes[:1]
	if err := ut.s.Measure(ctx, env); err != nil {
		t.Fatal(err)
	}
	if got, err := ut.s.Volumes(ctx, env.ID); err != nil || len(got) != 1 {
		t.Errorf("after a volume was removed: Volumes = %+v, %v; want db only", got, err)
	}

	// A failed measurement keeps the last one.
	fake.DiskUsageErr = errors.New("df failed")
	if err := ut.s.Measure(ctx, env); err == nil {
		t.Error("Measure with a failing daemon: err = nil")
	}
	if got, _ := ut.s.Volumes(ctx, env.ID); len(got) != 1 {
		t.Errorf("after a failed measurement: Volumes = %+v, want the last one kept", got)
	}
}

func TestMeasureRunning(t *testing.T) {
	t.Parallel()
	ut := newUsageTest(t, map[string]*dockertest.Fake{"local": {}, "nas": {}})
	if !ut.s.lock(ut.env("local").ID) {
		t.Fatal("lock = false")
	}
	if err := ut.s.Measure(context.Background(), ut.env("local")); !errors.Is(err, ErrRunning) {
		t.Errorf("Measure while running: err = %v, want ErrRunning", err)
	}
	if err := ut.s.Measure(context.Background(), ut.env("nas")); err != nil {
		t.Errorf("Measure of another environment: err = %v", err)
	}
}

func TestMeasureAllAndRun(t *testing.T) {
	t.Parallel()
	offline := &dockertest.Fake{Resources: dockertest.Resources{DiskUsageErr: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}}
	online := &dockertest.Fake{Resources: dockertest.Resources{
		Volumes:     []volume.Volume{{Name: "data"}},
		VolumeUsage: map[string]volume.UsageData{"data": {Size: 10, RefCount: 2}},
	}}
	ut := newUsageTest(t, map[string]*dockertest.Fake{"down": offline, "local": online})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ut.s.Run(ctx, time.Minute, time.Hour)
	}()
	if d := <-ut.clock.waits; d != time.Minute {
		t.Errorf("first wait = %v, want the delay", d)
	}
	ut.clock.after <- time.Time{}
	if d := <-ut.clock.waits; d != time.Hour {
		t.Errorf("next wait = %v, want the interval", d)
	}
	cancel()
	<-done

	got, err := ut.s.Volumes(context.Background(), ut.env("local").ID)
	if err != nil || len(got) != 1 || got["data"].Size == nil || *got["data"].Size != 10 {
		t.Errorf("online environment: Volumes = %+v, %v; want data measured", got, err)
	}
	if got, err := ut.s.Volumes(context.Background(), ut.env("down").ID); err != nil || len(got) != 0 {
		t.Errorf("offline environment: Volumes = %+v, %v; want none", got, err)
	}
}

func TestMeasureSaveError(t *testing.T) {
	t.Parallel()
	ut := newUsageTest(t, map[string]*dockertest.Fake{"local": {Resources: dockertest.Resources{Volumes: []volume.Volume{{Name: "db"}}}}})
	// An environment removed during the measurement: its rows can't be stored.
	gone := environments.Environment{ID: 999, Name: "local"}
	if err := ut.s.Measure(context.Background(), gone); !errors.Is(err, ErrSave) {
		t.Errorf("Measure of a removed environment: err = %v, want ErrSave", err)
	}
}
