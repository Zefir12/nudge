package metrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
)

var statsT0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// linuxStats is a reading at second sec with cumulative CPU counters, its previous
// reading one second earlier unless sec is 0. Over each second the container uses
// half of what the system's 2 CPUs did, so 100% of one CPU.
func linuxStats(sec int) container.StatsResponse {
	cpu := func(s int) container.CPUStats {
		return container.CPUStats{
			CPUUsage:    container.CPUUsage{TotalUsage: uint64(s) * 1e9},
			SystemUsage: uint64(s+1) * 2e9, OnlineCPUs: 2,
		}
	}
	s := container.StatsResponse{
		Read:     statsT0.Add(time.Duration(sec) * time.Second),
		CPUStats: cpu(sec),
		MemoryStats: container.MemoryStats{
			Usage: 300 << 20, Limit: 1 << 30, Stats: map[string]uint64{"inactive_file": 44 << 20},
		},
		Networks: map[string]container.NetworkStats{
			"eth0": {RxBytes: 1000, TxBytes: 200}, "eth1": {RxBytes: 24, TxBytes: 56},
		},
		BlkioStats: container.BlkioStats{IoServiceBytesRecursive: []container.BlkioStatEntry{
			{Op: "read", Value: 4096}, {Op: "Write", Value: 512}, {Op: "Read", Value: 4096}, {Op: "sync", Value: 9},
		}},
		PidsStats: container.PidsStats{Current: 7},
	}
	if sec > 0 {
		s.PreRead = s.Read.Add(-time.Second)
		s.PreCPUStats = cpu(sec - 1)
	}
	return s
}

func TestToSample(t *testing.T) {
	t.Parallel()
	want := Sample{
		Time: statsT0.Add(time.Second), CPUPercent: 100, MemoryUsage: 256 << 20, MemoryLimit: 1 << 30,
		MemoryPercent: 25, NetworkRx: 1024, NetworkTx: 256, BlockRead: 8192, BlockWrite: 512, PIDs: 7,
	}
	tests := []struct {
		name   string
		in     func(s *container.StatsResponse)
		want   func(w *Sample)
		wantOK bool
	}{
		{name: "cgroup v2", wantOK: true},
		{
			name: "cgroup v1 cache",
			in: func(s *container.StatsResponse) {
				s.MemoryStats.Stats = map[string]uint64{"total_inactive_file": 44 << 20}
			},
			wantOK: true,
		},
		{
			name: "cache larger than usage is ignored",
			in:   func(s *container.StatsResponse) { s.MemoryStats.Stats = map[string]uint64{"inactive_file": 400 << 20} },
			want: func(w *Sample) {
				w.MemoryUsage, w.MemoryPercent = 300<<20, 300.0/1024*100
			},
			wantOK: true,
		},
		{
			name: "online CPUs missing: counts per-CPU usage",
			in: func(s *container.StatsResponse) {
				s.CPUStats.OnlineCPUs = 0
				s.CPUStats.CPUUsage.PercpuUsage = []uint64{1, 2, 3, 4}
			},
			want:   func(w *Sample) { w.CPUPercent = 200 },
			wantOK: true,
		},
		{
			name:   "no memory limit",
			in:     func(s *container.StatsResponse) { s.MemoryStats.Limit = 0 },
			want:   func(w *Sample) { w.MemoryLimit, w.MemoryPercent = 0, 0 },
			wantOK: true,
		},
		{
			name:   "idle",
			in:     func(s *container.StatsResponse) { s.PreCPUStats.CPUUsage = s.CPUStats.CPUUsage },
			want:   func(w *Sample) { w.CPUPercent = 0 },
			wantOK: true,
		},
		{
			name: "no previous reading",
			in:   func(s *container.StatsResponse) { s.PreCPUStats = container.CPUStats{} },
		},
		{
			name: "windows",
			in: func(s *container.StatsResponse) {
				s.OSType, s.NumProcs = "windows", 4
				// 1s is 1e7 units of 100ns on each of 4 processors; 2e7 used is 50%.
				s.CPUStats = container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 3e7}}
				s.PreCPUStats = container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 1e7}}
				s.MemoryStats = container.MemoryStats{PrivateWorkingSet: 64 << 20}
				s.BlkioStats = container.BlkioStats{}
				s.StorageStats = container.StorageStats{ReadSizeBytes: 10, WriteSizeBytes: 20}
			},
			want: func(w *Sample) {
				w.CPUPercent, w.MemoryUsage, w.MemoryLimit, w.MemoryPercent = 50, 64<<20, 0, 0
				w.BlockRead, w.BlockWrite = 10, 20
			},
			wantOK: true,
		},
		{
			name: "windows without a previous reading",
			in:   func(s *container.StatsResponse) { s.OSType, s.PreRead = "windows", time.Time{} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := linuxStats(1)
			if tt.in != nil {
				tt.in(&in)
			}
			w := want
			if tt.want != nil {
				tt.want(&w)
			}
			got, ok := ToSample(in)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != w {
				t.Errorf("sample =\n%+v\nwant\n%+v", got, w)
			}
		})
	}
}

// fakeClock is settable; each After is a channel that tick fires.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiting []chan time.Time
	// waits are the durations of every After so far.
	waits []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	ch := make(chan time.Time, 1)
	c.waiting = append(c.waiting, ch)
	return ch
}

// tick moves the clock by d and ends every wait.
func (c *fakeClock) tick(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, ch := range c.waiting {
		ch <- c.now
	}
	c.waiting = nil
}

// wait returns the duration of the nth After (from 1), waiting for it to be asked for.
func (c *fakeClock) wait(t *testing.T, n int) time.Duration {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		if len(c.waits) >= n {
			d := c.waits[n-1]
			c.mu.Unlock()
			return d
		}
		c.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("After #%d never asked for", n)
		}
		time.Sleep(time.Millisecond)
	}
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// fakeEnvs lists envs and hands out a Docker fake per environment ID.
type fakeEnvs struct {
	mu    sync.Mutex
	envs  []environments.Environment
	fakes map[int64]*dockertest.Fake
}

func (f *fakeEnvs) List(context.Context) ([]environments.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.envs), nil
}

func (f *fakeEnvs) Client(env environments.Environment) (docker.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.fakes[env.ID]
	if !ok {
		return nil, fmt.Errorf("no fake for %d", env.ID)
	}
	return c, nil
}

func (f *fakeEnvs) setList(ids ...int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.envs = nil
	for _, id := range ids {
		f.envs = append(f.envs, environments.Environment{ID: id})
	}
}

// t0 is on the hour, so ticks are easy to place in hours.
var t0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

type metricsTest struct {
	svc   *Service
	db    *sql.DB
	clock *fakeClock
	envs  *fakeEnvs
	fake  *dockertest.Fake
}

// newMetricsTest has environment 1 with the running containers web (bob's), db (no
// owner) and gone (no stats: it stopped), and the stopped old; owners are by label.
func newMetricsTest(t *testing.T) metricsTest {
	t.Helper()
	db, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fake := &dockertest.Fake{Resources: dockertest.Resources{
		Containers: []container.Summary{
			{ID: "w1", Names: []string{"/web"}, State: container.StateRunning, Labels: map[string]string{"owner": "bob"}},
			{ID: "d1", Names: []string{"/db"}, State: container.StateRunning},
			{ID: "g1", Names: []string{"/gone"}, State: container.StateRunning},
			{ID: "o1", Names: []string{"/old"}, State: container.StateExited},
		},
		Stats: map[string][]container.StatsResponse{"w1": {linuxStats(1)}, "d1": {linuxStats(1)}, "o1": {linuxStats(1)}},
	}}
	envs := &fakeEnvs{fakes: map[int64]*dockertest.Fake{1: fake, 2: fake}}
	envs.setList(1)
	clock := &fakeClock{now: t0}
	owners := func(_ context.Context, _ docker.Client, items []container.Summary) (map[string]string, error) {
		out := map[string]string{}
		for _, it := range items {
			if k := it.Labels["owner"]; k != "" {
				out[it.ID] = k
			}
		}
		return out, nil
	}
	svc := New(Options{DB: db, Envs: envs, Owners: owners, Clock: clock, Interval: time.Minute, Retention: 7 * 24 * time.Hour})
	return metricsTest{svc: svc, db: db, clock: clock, envs: envs, fake: fake}
}

func (mt metricsTest) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := mt.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A tick stores every sampled running container with its owner and the environment's
// totals; one that couldn't be sampled is left out.
func TestCollect(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	if err := mt.svc.Collect(t.Context(), environments.Environment{ID: 1}); err != nil {
		t.Fatal(err)
	}
	rows, err := mt.db.Query(`SELECT container_id, time, name, owner, cpu, memory, network_rx, block_write FROM container_samples ORDER BY container_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var id, name, owner string
		var at, mem, rx, bw int64
		var cpu float64
		if err := rows.Scan(&id, &at, &name, &owner, &cpu, &mem, &rx, &bw); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %d %s %q %.0f %d %d %d", id, at-t0.Unix(), name, owner, cpu, mem, rx, bw))
	}
	want := []string{`d1 0 db "" 100 268435456 1024 512`, `w1 0 web "bob" 100 268435456 1024 512`}
	if !slices.Equal(got, want) {
		t.Errorf("container rows =\n%v\nwant\n%v", got, want)
	}
	var cpu float64
	var mem, n int64
	if err := mt.db.QueryRow(`SELECT cpu, memory, containers FROM environment_samples WHERE environment_id = 1 AND time = ?`, t0.Unix()).Scan(&cpu, &mem, &n); err != nil {
		t.Fatal(err)
	}
	if cpu != 200 || mem != 512<<20 || n != 2 {
		t.Errorf("environment row = %v %v %v, want 200%% of 512 MiB over 2", cpu, mem, n)
	}
}

// A tick where nothing could be sampled, or the host is unreachable, stores nothing.
func TestCollectRefused(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	mt.fake.Stats = nil
	if err := mt.svc.Collect(t.Context(), environments.Environment{ID: 1}); !errors.Is(err, errNoneSampled) {
		t.Errorf("err = %v, want none sampled", err)
	}
	mt.fake.ResourceErr = &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	if err := mt.svc.Collect(t.Context(), environments.Environment{ID: 1}); !docker.Unreachable(err) {
		t.Errorf("err = %v, want unreachable", err)
	}
	if n := mt.count(t, `SELECT COUNT(*) FROM environment_samples`); n != 0 {
		t.Errorf("environment rows = %d, want none", n)
	}
}

// Latest answers the newest tick, a user's share of it, and nothing once it's stale or
// collection is off.
func TestLatest(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	ctx := t.Context()
	if _, ok, err := mt.svc.Latest(ctx, 1, Scope{All: true}); ok || err != nil {
		t.Fatalf("latest before a tick = %v %v, want none", ok, err)
	}
	if err := mt.svc.Collect(ctx, environments.Environment{ID: 1}); err != nil {
		t.Fatal(err)
	}
	mt.clock.set(t0.Add(time.Minute))
	tests := []struct {
		name  string
		scope Scope
		want  Summary
	}{
		{name: "all", scope: Scope{All: true}, want: Summary{SampledAt: t0, CPUPercent: 200, MemoryUsage: 512 << 20, Containers: 2}},
		{name: "bob", scope: Scope{Owner: "bob"}, want: Summary{SampledAt: t0, CPUPercent: 100, MemoryUsage: 256 << 20, Containers: 1}},
		{name: "nobody", scope: Scope{Owner: "carol"}, want: Summary{SampledAt: t0}},
		{name: "empty owner is nobody", scope: Scope{}, want: Summary{SampledAt: t0}},
	}
	for _, tt := range tests {
		got, ok, err := mt.svc.Latest(ctx, 1, tt.scope)
		if err != nil || !ok || got != tt.want {
			t.Errorf("%s: latest = %+v %v %v, want %+v", tt.name, got, ok, err, tt.want)
		}
	}
	mt.clock.set(t0.Add(staleTicks*time.Minute + time.Second))
	if _, ok, _ := mt.svc.Latest(ctx, 1, Scope{All: true}); ok {
		t.Error("stale tick answered")
	}
	off := New(Options{DB: mt.db, Clock: mt.clock})
	mt.clock.set(t0)
	if _, ok, _ := off.Latest(ctx, 1, Scope{All: true}); ok {
		t.Error("answered with collection off")
	}
}

// insert stores a tick of env at at: container id (owner) with cpu and memory, and the
// environment's totals of it.
func (mt metricsTest) insert(t *testing.T, env int64, at time.Time, containers ...Container) {
	t.Helper()
	owners := map[string]string{}
	for _, c := range containers {
		if c.Name != "" {
			owners[c.ID] = c.Name
		}
	}
	if err := mt.svc.save(t.Context(), env, at, containers, owners, nil); err != nil {
		t.Fatal(err)
	}
}

func ctr(id, owner string, cpu float64, mem, rx uint64) Container {
	return Container{ID: id, Name: owner, Sample: Sample{CPUPercent: cpu, MemoryUsage: mem, NetworkRx: rx}}
}

// Cleanup rolls finished hours up (average, maximum, last counter), leaves the current
// hour raw, drops raw rows past RawRetention, rollups past the retention and every row
// of a removed environment; running it again changes nothing.
func TestCleanup(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	old := t0.Add(-3 * 24 * time.Hour)
	// Two ticks in the hour before t0, one in t0's hour; one tick three days ago (raw
	// past RawRetention) and one ten days ago (past the 7-day retention).
	mt.insert(t, 1, t0.Add(-50*time.Minute), ctr("a", "bob", 10, 100, 1), ctr("b", "", 30, 300, 5))
	mt.insert(t, 1, t0.Add(-20*time.Minute), ctr("a", "bob", 20, 200, 2))
	mt.insert(t, 1, t0.Add(10*time.Minute), ctr("a", "bob", 90, 900, 3))
	mt.insert(t, 1, old, ctr("a", "bob", 5, 50, 1))
	mt.insert(t, 1, t0.Add(-10*24*time.Hour), ctr("a", "bob", 5, 50, 1))
	mt.insert(t, 2, t0.Add(-50*time.Minute), ctr("z", "", 1, 1, 1))
	for _, r := range []struct {
		env  int64
		hour time.Time
	}{{1, old}, {1, t0.Add(-10 * 24 * time.Hour)}, {2, t0}} {
		if _, err := mt.db.Exec(`INSERT INTO activity_hours (environment_id, hour, owner, count) VALUES (?, ?, '', 1)`, r.env, r.hour.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	now := t0.Add(15 * time.Minute)
	for range 2 {
		if err := mt.svc.Cleanup(t.Context(), now, []int64{1}); err != nil {
			t.Fatal(err)
		}
		var cpuAvg, cpuMax, memAvg float64
		var memMax, rx, n, samples int64
		if err := mt.db.QueryRow(`SELECT cpu_avg, cpu_max, memory_avg, memory_max, network_rx, containers, samples FROM environment_hours
			WHERE environment_id = 1 AND hour = ?`, t0.Add(-time.Hour).Unix()).Scan(&cpuAvg, &cpuMax, &memAvg, &memMax, &rx, &n, &samples); err != nil {
			t.Fatal(err)
		}
		if cpuAvg != 30 || cpuMax != 40 || memAvg != 300 || memMax != 400 || rx != 6 || n != 2 || samples != 2 {
			t.Errorf("hour = cpu %v/%v mem %v/%v rx %v containers %v samples %v", cpuAvg, cpuMax, memAvg, memMax, rx, n, samples)
		}
		if err := mt.db.QueryRow(`SELECT cpu_avg, cpu_max, samples FROM container_hours
			WHERE environment_id = 1 AND container_id = 'a' AND hour = ?`, t0.Add(-time.Hour).Unix()).Scan(&cpuAvg, &cpuMax, &samples); err != nil {
			t.Fatal(err)
		}
		if cpuAvg != 15 || cpuMax != 20 || samples != 2 {
			t.Errorf("container hour = cpu %v/%v samples %v", cpuAvg, cpuMax, samples)
		}
		checks := []struct {
			name  string
			query string
			arg   int64
			want  int
		}{
			{"current hour not rolled up", `SELECT COUNT(*) FROM environment_hours WHERE hour = ?`, t0.Unix(), 0},
			{"current hour raw", `SELECT COUNT(*) FROM environment_samples WHERE time = ?`, t0.Add(10 * time.Minute).Unix(), 1},
			{"old raw dropped", `SELECT COUNT(*) FROM environment_samples WHERE time <= ?`, old.Unix(), 0},
			{"old raw dropped (containers)", `SELECT COUNT(*) FROM container_samples WHERE time <= ?`, old.Unix(), 0},
			{"old hour kept as rollup", `SELECT COUNT(*) FROM container_hours WHERE hour = ?`, old.Unix(), 1},
			{"rollups past retention dropped", `SELECT COUNT(*) FROM environment_hours WHERE hour < ?`, t0.Add(-7 * 24 * time.Hour).Unix(), 0},
			{"removed environment", `SELECT COUNT(*) FROM container_samples WHERE environment_id = 2 OR ? = 0`, 1, 0},
			{"removed environment rollups", `SELECT COUNT(*) FROM environment_hours WHERE environment_id = 2 OR ? = 0`, 1, 0},
			{"activity kept", `SELECT COUNT(*) FROM activity_hours WHERE hour = ?`, old.Unix(), 1},
			{"activity past retention dropped", `SELECT COUNT(*) FROM activity_hours WHERE hour < ?`, t0.Add(-7 * 24 * time.Hour).Unix(), 0},
			{"removed environment activity", `SELECT COUNT(*) FROM activity_hours WHERE environment_id = 2 OR ? = 0`, 1, 0},
		}
		for _, c := range checks {
			if n := mt.count(t, c.query, c.arg); n != c.want {
				t.Errorf("%s: %d rows, want %d", c.name, n, c.want)
			}
		}
	}
}

// History answers raw samples within RawRetention and rollups beyond, in buckets of the
// step asked for, of the environment, a user's containers or one container.
func TestHistory(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	// Ticks at t0-50m, t0-20m (rolled up into t0-1h) and t0+10m, t0+11m (raw only).
	mt.insert(t, 1, t0.Add(-50*time.Minute), ctr("a", "bob", 10, 100, 1), ctr("b", "", 30, 300, 5))
	mt.insert(t, 1, t0.Add(-20*time.Minute), ctr("a", "bob", 20, 200, 2), ctr("b", "", 10, 100, 6))
	mt.insert(t, 1, t0.Add(10*time.Minute), ctr("a", "bob", 40, 400, 3))
	mt.insert(t, 1, t0.Add(11*time.Minute), ctr("a", "bob", 60, 600, 4), ctr("c", "carol", 1, 1, 1))
	mt.insert(t, 1, t0.Add(-5*24*time.Hour), ctr("a", "bob", 7, 70, 1))
	if err := mt.svc.Cleanup(t.Context(), t0.Add(15*time.Minute), []int64{1}); err != nil {
		t.Fatal(err)
	}
	mt.clock.set(t0.Add(15 * time.Minute))
	type pt struct {
		at       time.Duration
		cpu, max float64
		mem, rx  uint64
	}
	tests := []struct {
		name   string
		q      Query
		source string
		step   int64
		want   []pt
	}{
		{
			name: "environment raw", q: Query{Env: 1, Scope: Scope{All: true}, Since: t0.Add(-time.Hour)},
			source: SourceRaw, step: 60,
			want: []pt{{-50 * time.Minute, 40, 40, 400, 6}, {-20 * time.Minute, 30, 30, 300, 8}, {10 * time.Minute, 40, 40, 400, 3}, {11 * time.Minute, 61, 61, 601, 5}},
		},
		{
			name: "environment in 30m buckets", q: Query{Env: 1, Scope: Scope{All: true}, Since: t0.Add(-time.Hour), Step: 30 * time.Minute},
			source: SourceRaw, step: 1800,
			want: []pt{{-time.Hour, 40, 40, 400, 6}, {-30 * time.Minute, 30, 30, 300, 8}, {0, 50.5, 61, 500, 5}},
		},
		{
			name: "bob raw", q: Query{Env: 1, Scope: Scope{Owner: "bob"}, Since: t0.Add(-time.Hour)},
			source: SourceRaw, step: 60,
			want: []pt{{-50 * time.Minute, 10, 10, 100, 1}, {-20 * time.Minute, 20, 20, 200, 2}, {10 * time.Minute, 40, 40, 400, 3}, {11 * time.Minute, 60, 60, 600, 4}},
		},
		{
			name: "container b raw", q: Query{Env: 1, Container: "b", Scope: Scope{Owner: "nobody"}, Since: t0.Add(-time.Hour)},
			source: SourceRaw, step: 60,
			want: []pt{{-50 * time.Minute, 30, 30, 300, 5}, {-20 * time.Minute, 10, 10, 100, 6}},
		},
		{
			name: "environment hourly", q: Query{Env: 1, Scope: Scope{All: true}, Since: t0.Add(-3 * 24 * time.Hour)},
			source: SourceHourly, step: 3600,
			want: []pt{{-time.Hour, 35, 40, 350, 8}},
		},
		{
			name: "bob hourly from far back, clamped to the retention", q: Query{Env: 1, Scope: Scope{Owner: "bob"}, Since: t0.Add(-365 * 24 * time.Hour), Step: 90 * time.Minute},
			source: SourceHourly, step: 7200,
			want: []pt{{-5*24*time.Hour - 0, 7, 7, 70, 1}, {-2 * time.Hour, 15, 20, 150, 2}},
		},
		{
			name: "nobody's", q: Query{Env: 1, Scope: Scope{}, Since: t0.Add(-time.Hour)},
			source: SourceRaw, step: 60,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := mt.svc.History(t.Context(), tt.q)
			if err != nil {
				t.Fatal(err)
			}
			if h.Source != tt.source || h.Step != tt.step {
				t.Errorf("source %s step %d, want %s %d", h.Source, h.Step, tt.source, tt.step)
			}
			var got []pt
			for _, p := range h.Points {
				got = append(got, pt{p.Time.Sub(t0), p.CPUPercent, p.CPUMax, p.MemoryUsage, p.NetworkRx})
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("points =\n%v\nwant\n%v", got, tt.want)
			}
		})
	}
}

// A long span at a fine step is coarsened to MaxPoints buckets.
func TestHistoryMaxPoints(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	svc := New(Options{DB: mt.db, Clock: mt.clock, Interval: 10 * time.Second})
	h, err := svc.History(t.Context(), Query{Env: 1, Scope: Scope{All: true}, Since: t0.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(24*3600+MaxPoints-1) / MaxPoints; h.Step != want || h.Source != SourceRaw {
		t.Errorf("step = %d %s, want %d raw", h.Step, h.Source, want)
	}
}

// Run starts a collector per environment, follows the list as environments come and
// go, cleans up removed ones' rows, and stops with its context.
func TestRun(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		mt.svc.Run(ctx)
	}()
	waitFor := func(what, query string, want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for mt.count(t, query) != want {
			if time.Now().After(deadline) {
				t.Fatalf("%s: never got %d rows", what, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor("first tick of 1", `SELECT COUNT(*) FROM environment_samples WHERE environment_id = 1`, 1)
	mt.envs.setList(2)
	// Past cleanupEvery, so the next round also drops environment 1's rows. Ticks may
	// find a collector still between waits; keep ticking until the rows settle.
	deadline := time.Now().Add(5 * time.Second)
	for mt.count(t, `SELECT COUNT(*) FROM environment_samples WHERE environment_id = 1`) != 0 ||
		mt.count(t, `SELECT COUNT(*) FROM environment_samples WHERE environment_id = 2`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("environment 2 never collected, or 1's rows never dropped")
		}
		mt.clock.tick(cleanupEvery)
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// With collection off Run returns at once.
func TestRunOff(t *testing.T) {
	t.Parallel()
	mt := newMetricsTest(t)
	New(Options{DB: mt.db, Envs: mt.envs, Clock: mt.clock}).Run(t.Context())
	if n := mt.count(t, `SELECT COUNT(*) FROM environment_samples`); n != 0 {
		t.Errorf("rows = %d, want none", n)
	}
}
