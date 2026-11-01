package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/moby/moby/api/types/container"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/metrics"
)

var metricsNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// metricsEnvs lists list to the collector and hands it one fake for every environment.
type metricsEnvs struct {
	fake *dockertest.Fake
	list []environments.Environment
}

func (e *metricsEnvs) List(context.Context) ([]environments.Environment, error) {
	return e.list, nil
}
func (e *metricsEnvs) Client(environments.Environment) (docker.Client, error) {
	return e.fake, nil
}

// withMetrics gives the API a metrics service over a fresh metrics.db, collecting from
// envs.fake, its clock and the API's standing at metricsNow.
func withMetrics(t *testing.T, envs *metricsEnvs, svc **metrics.Service) func(*Options) {
	t.Helper()
	db, err := metrics.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := fixedClock{now: metricsNow}
	*svc = metrics.New(metrics.Options{DB: db, Envs: envs, Owners: ContainerOwners, Clock: clock, Interval: time.Minute})
	return func(o *Options) {
		o.Metrics = *svc
		o.Now = clock.Now
	}
}

func getJSON[T any](t *testing.T, h http.Handler, path string, cookie *http.Cookie) T {
	t.Helper()
	rec := do(t, h, request{method: "GET", path: path, cookie: cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The summary is the collector's latest tick; the histories answer the stored ticks of
// the environment and of one container.
func TestEnvironmentMetrics(t *testing.T) {
	t.Parallel()
	envs := &metricsEnvs{}
	var svc *metrics.Service
	st := newStatsTestOpts(t, withMetrics(t, envs, &svc))
	envs.fake = st.fake
	local := st.ids["local"]
	if rec := do(t, st.h, request{method: "GET", path: st.path("local", "/stats/summary"), cookie: st.cookie}); rec.Code != http.StatusNotFound {
		t.Errorf("summary before a tick = %d %s, want 404", rec.Code, rec.Body)
	}
	if err := svc.Collect(t.Context(), environments.Environment{ID: local}); err != nil {
		t.Fatal(err)
	}
	sum := getJSON[metrics.Summary](t, st.h, st.path("local", "/stats/summary"), st.cookie)
	if want := (metrics.Summary{SampledAt: metricsNow, CPUPercent: 200, MemoryUsage: 512 << 20, Containers: 2}); sum != want {
		t.Errorf("summary = %+v, want %+v", sum, want)
	}
	h := getJSON[metrics.History](t, st.h, st.path("local", "/metrics?since=1h"), st.cookie)
	if h.Source != metrics.SourceRaw || len(h.Points) != 1 || h.Points[0].CPUPercent != 200 || !h.Points[0].Time.Equal(metricsNow) {
		t.Errorf("environment history = %+v", h)
	}
	h = getJSON[metrics.History](t, st.h, st.path("local", "/containers/web/metrics?since="+metricsNow.Add(-time.Hour).Format(time.RFC3339)+"&resolution=5m"), st.cookie)
	if h.Step != 300 || len(h.Points) != 1 || h.Points[0].CPUPercent != 100 || h.Points[0].MemoryUsage != 256<<20 {
		t.Errorf("container history = %+v", h)
	}
	h = getJSON[metrics.History](t, st.h, st.path("local", "/metrics?since=72h"), st.cookie)
	if h.Source != metrics.SourceHourly || len(h.Points) != 0 {
		t.Errorf("history beyond the raw samples = %+v, want empty rollups (none yet)", h)
	}

	tests := []struct {
		name, path, want string
		code             int
	}{
		{name: "bad since", path: st.path("local", "/metrics?since=yesterday"), code: http.StatusBadRequest, want: "since:"},
		{name: "negative since", path: st.path("local", "/metrics?since=-1h"), code: http.StatusBadRequest, want: "since:"},
		{name: "bad resolution", path: st.path("local", "/metrics?resolution=0s"), code: http.StatusBadRequest, want: "resolution:"},
		{name: "unknown container", path: st.path("local", "/containers/nope/metrics"), code: http.StatusNotFound, want: "container not found"},
		{name: "offline container", path: st.path("down", "/containers/web/metrics"), code: http.StatusServiceUnavailable, want: "the environment is offline"},
		{name: "unknown environment", path: "/api/v1/environments/999/metrics", code: http.StatusNotFound, want: "environment not found"},
	}
	for _, tt := range tests {
		rec := do(t, st.h, request{method: "GET", path: tt.path, cookie: st.cookie})
		var body struct{ Error string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != tt.code || len(body.Error) < len(tt.want) || body.Error[:len(tt.want)] != tt.want {
			t.Errorf("%s: %d %s, want %d %q", tt.name, rec.Code, rec.Body, tt.code, tt.want)
		}
	}
}

// A user's summary and history are of their own containers: not another user's, not
// the admins', not one whose image forges their owner label.
func TestOwnershipMetrics(t *testing.T) {
	t.Parallel()
	envs := &metricsEnvs{}
	var svc *metrics.Service
	ot := newOwnTestOpts(t, withMetrics(t, envs, &svc))
	envs.fake = ot.fake
	for _, name := range ownedObjects {
		ot.fake.Stats[name] = []container.StatsResponse{linuxStats(1)}
	}
	if err := svc.Collect(t.Context(), environments.Environment{ID: ot.local}); err != nil {
		t.Fatal(err)
	}
	if sum := getJSON[metrics.Summary](t, ot.h, ot.path(ot.local, "/stats/summary"), ot.admin); sum.Containers != 4 || sum.CPUPercent != 400 {
		t.Errorf("admin's summary = %+v, want all 4 containers", sum)
	}
	if sum := getJSON[metrics.Summary](t, ot.h, ot.path(ot.local, "/stats/summary"), ot.bob); sum.Containers != 1 || sum.CPUPercent != 100 {
		t.Errorf("bob's summary = %+v, want his one container", sum)
	}
	h := getJSON[metrics.History](t, ot.h, ot.path(ot.local, "/metrics"), ot.bob)
	if len(h.Points) != 1 || h.Points[0].CPUPercent != 100 || h.Points[0].MemoryUsage != 256<<20 {
		t.Errorf("bob's history = %+v, want his container's", h)
	}
	if h := getJSON[metrics.History](t, ot.h, ot.path(ot.local, "/containers/mine/metrics"), ot.bob); len(h.Points) != 1 {
		t.Errorf("bob's container history = %+v", h)
	}
}

// The ownership rule the collector files samples under.
func TestContainerOwners(t *testing.T) {
	t.Parallel()
	ot := newOwnTest(t)
	got, err := ContainerOwners(t.Context(), ot.fake, ot.fake.Containers)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["mine"] != ot.bobKey || got["alices"] != ot.alice {
		t.Errorf("owners = %v, want mine (bob) and alices (alice), not forged", got)
	}
}

// The live stream sends each tick within the caller's scope, and ends with an error
// frame when collection stops; it is refused while collection is off.
func TestLiveMetrics(t *testing.T) {
	t.Parallel()
	envs := &metricsEnvs{}
	var svc *metrics.Service
	ot := newOwnTestOpts(t, withMetrics(t, envs, &svc))
	envs.fake = ot.fake
	envs.list = []environments.Environment{{ID: ot.local}}
	for _, name := range ownedObjects {
		ot.fake.Stats[name] = []container.StatsResponse{linuxStats(1)}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	path := ot.path(ot.local, "/metrics/live")
	dial := func(cookie *http.Cookie) *websocket.Conn {
		lt := logsTest{envTest: envTest{h: ot.h, cookie: cookie}, srv: ot.srv}
		ws, err := lt.dial(ctx, t, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ws.CloseNow() })
		return ws
	}
	admin, bob := dial(ot.admin), dial(ot.bob)

	runCtx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(runCtx)
	}()
	read := func(ws *websocket.Conn) metrics.Live {
		t.Helper()
		var l metrics.Live
		if err := wsjson.Read(ctx, ws, &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := read(admin); len(l.Containers) != 4 || l.Total.CPUPercent != 400 || !l.Time.Equal(metricsNow) {
		t.Errorf("admin's tick = %+v, want all 4 containers", l)
	}
	if l := read(bob); len(l.Containers) != 1 || l.Containers[0].ID != "mine" || l.Total.CPUPercent != 100 {
		t.Errorf("bob's tick = %+v, want his one container", l)
	}

	stop()
	<-done
	frames := readAll(ctx, t, bob)
	if len(frames) != 1 || frames[0]["error"] != "usage collection of the environment stopped" {
		t.Errorf("frames after collection stopped = %v", frames)
	}

	off := newOwnTestOpts(t, func(o *Options) {
		o.Metrics = metrics.New(metrics.Options{}) // Interval 0: collection off
	})
	if rec := do(t, off.h, request{method: "GET", path: off.path(off.local, "/metrics/live"), cookie: off.admin}); rec.Code != http.StatusNotFound {
		t.Errorf("collection off = %d %s, want 404", rec.Code, rec.Body)
	}
}
