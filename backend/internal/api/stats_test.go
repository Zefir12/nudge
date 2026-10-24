package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
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

func TestToStatsSample(t *testing.T) {
	t.Parallel()
	want := statsSample{
		Time: statsT0.Add(time.Second), CPUPercent: 100, MemoryUsage: 256 << 20, MemoryLimit: 1 << 30,
		MemoryPercent: 25, NetworkRx: 1024, NetworkTx: 256, BlockRead: 8192, BlockWrite: 512, PIDs: 7,
	}
	tests := []struct {
		name   string
		in     func(s *container.StatsResponse)
		want   func(w *statsSample)
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
			want: func(w *statsSample) {
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
			want:   func(w *statsSample) { w.CPUPercent = 200 },
			wantOK: true,
		},
		{
			name:   "no memory limit",
			in:     func(s *container.StatsResponse) { s.MemoryStats.Limit = 0 },
			want:   func(w *statsSample) { w.MemoryLimit, w.MemoryPercent = 0, 0 },
			wantOK: true,
		},
		{
			name:   "idle",
			in:     func(s *container.StatsResponse) { s.PreCPUStats.CPUUsage = s.CPUStats.CPUUsage },
			want:   func(w *statsSample) { w.CPUPercent = 0 },
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
			want: func(w *statsSample) {
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
			got, ok := toStatsSample(in)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != w {
				t.Errorf("sample =\n%+v\nwant\n%+v", got, w)
			}
		})
	}
}

type statsTest struct {
	envTest
	srv  *httptest.Server
	fake *dockertest.Fake
}

func newStatsTest(t *testing.T) statsTest {
	t.Helper()
	running := &container.State{Running: true}
	fake := &dockertest.Fake{Resources: dockertest.Resources{
		Containers: []container.Summary{
			{ID: "bbb", Names: []string{"/web"}, State: container.StateRunning},
			{ID: "aaa", Names: []string{"/db"}, State: container.StateRunning},
			{ID: "ccc", Names: []string{"/gone"}, State: container.StateRunning},
			{ID: "ddd", Names: []string{"/stopped"}, State: container.StateExited},
		},
		ContainerDetails: map[string]container.InspectResponse{
			"web":     {ID: "bbb", State: running},
			"stopped": {ID: "ddd", State: &container.State{Status: container.StateExited}},
		},
		Stats: map[string][]container.StatsResponse{
			"web": {linuxStats(0), linuxStats(1), linuxStats(2)},
			"bbb": {linuxStats(1)},
			"aaa": {linuxStats(1)},
			"ddd": {linuxStats(1)},
		},
		StatsCancelled: make(chan string, 1),
	}}
	broken := &dockertest.Fake{Resources: dockertest.Resources{
		ContainerDetails: map[string]container.InspectResponse{"web": {State: running}},
		StatsErr:         errors.New("daemon: secret-detail"),
	}}
	et := newEnvTest(t, map[string]*dockertest.Fake{
		"local":  fake,
		"down":   {Resources: dockertest.Resources{ResourceErr: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}},
		"broken": broken,
	})
	srv := httptest.NewServer(et.h)
	t.Cleanup(srv.Close)
	return statsTest{envTest: et, srv: srv, fake: fake}
}

func (st statsTest) path(env, rest string) string {
	return "/api/v1/environments/" + strconv.FormatInt(st.ids[env], 10) + rest
}

func (st statsTest) dial(ctx context.Context, t *testing.T, path string) *websocket.Conn {
	t.Helper()
	lt := logsTest{envTest: st.envTest, srv: st.srv}
	ws, err := lt.dial(ctx, t, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

// The first reading of a stream has no previous one, so it is skipped; the stream
// then follows the daemon until the client leaves.
func TestContainerStatsStream(t *testing.T) {
	t.Parallel()
	st := newStatsTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws := st.dial(ctx, t, st.path("local", "/containers/web/stats"))
	var got []statsSample
	for range 2 {
		_, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var s statsSample
		if err := json.Unmarshal(data, &s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	for i, s := range got {
		if want := statsT0.Add(time.Duration(i+1) * time.Second); !s.Time.Equal(want) || s.CPUPercent != 100 || s.MemoryUsage != 256<<20 {
			t.Errorf("sample %d = %+v, want time %v, 100%% CPU, 256 MiB", i, s, want)
		}
	}
	if opts := st.fake.StatsOptions(); len(opts) != 1 || opts[0] != (client.ContainerStatsOptions{Stream: true}) {
		t.Errorf("options = %+v, want one streamed call", opts)
	}
	if err := ws.Close(websocket.StatusNormalClosure, "tab closed"); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-st.fake.StatsCancelled:
		if id != "web" {
			t.Errorf("cancelled %q", id)
		}
	case <-ctx.Done():
		t.Fatal("the Docker stream was not cancelled")
	}
}

func TestContainerStatsStreamError(t *testing.T) {
	t.Parallel()
	st := newStatsTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws := st.dial(ctx, t, st.path("broken", "/containers/web/stats"))
	got, _ := json.Marshal(readAll(ctx, t, ws))
	if want := `[{"error":"the environment's Docker host ended the stats stream with an error"}]`; string(got) != want {
		t.Errorf("frames = %s, want %s", got, want)
	}
}

func TestContainerStatsRefused(t *testing.T) {
	t.Parallel()
	st := newStatsTest(t)
	tests := []struct {
		name   string
		path   string
		cookie bool
		code   int
		want   string
	}{
		{name: "no session", path: st.path("local", "/containers/web/stats"), code: http.StatusUnauthorized, want: "authentication required"},
		{name: "unknown container", path: st.path("local", "/containers/nope/stats"), cookie: true, code: http.StatusNotFound, want: "container not found"},
		{name: "bad id", path: st.path("local", "/containers/..x/stats"), cookie: true, code: http.StatusNotFound, want: "container not found"},
		{name: "stopped", path: st.path("local", "/containers/stopped/stats"), cookie: true, code: http.StatusConflict, want: "container is not running"},
		{name: "offline", path: st.path("down", "/containers/web/stats"), cookie: true, code: http.StatusServiceUnavailable, want: "the environment is offline"},
		{name: "environment offline", path: st.path("down", "/stats"), cookie: true, code: http.StatusServiceUnavailable, want: "the environment is offline"},
		{name: "environment without session", path: st.path("local", "/stats"), code: http.StatusUnauthorized, want: "authentication required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := request{method: "GET", path: tt.path}
			if tt.cookie {
				req.cookie = st.cookie
			}
			rec := do(t, st.h, req)
			if rec.Code != tt.code {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tt.code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), `"error":"`+tt.want+`"`) {
				t.Errorf("body = %s, want error %q", rec.Body, tt.want)
			}
		})
	}
}

// The overview samples running containers only, leaves out one without stats (it
// stopped meanwhile) and sorts by name.
func TestEnvironmentStats(t *testing.T) {
	t.Parallel()
	st := newStatsTest(t)
	rec := do(t, st.h, request{method: "GET", path: st.path("local", "/stats"), cookie: st.cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", rec.Code, rec.Body)
	}
	var got environmentStats
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	sample, _ := toStatsSample(linuxStats(1))
	want := environmentStats{
		Containers: []containerUsage{{ID: "aaa", Name: "db", statsSample: sample}, {ID: "bbb", Name: "web", statsSample: sample}},
		Total:      usageTotals{CPUPercent: 200, MemoryUsage: 512 << 20, NetworkRx: 2048, NetworkTx: 512, BlockRead: 16384, BlockWrite: 1024},
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("stats =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
	for _, o := range st.fake.StatsOptions() {
		if o != (client.ContainerStatsOptions{IncludePreviousSample: true}) {
			t.Errorf("options = %+v, want one-shot with a previous sample", o)
		}
	}
}
