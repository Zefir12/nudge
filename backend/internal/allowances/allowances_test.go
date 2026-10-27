package allowances

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

const gib = 1 << 30

// oneCPU8G is the issue's example: 1 vCPU, 8 GiB, 20 GiB of disk, 5 containers, ports
// 8000-8009.
var oneCPU8G = Allowance{CPUsMilli: 1000, MemoryBytes: 8 * gib, DiskBytes: 20 * gib, MaxContainers: 5, PortMin: 8000, PortMax: 8009}

func TestCheck(t *testing.T) {
	t.Parallel()
	small := Request{Image: "nginx:1", CPUsMilli: 250, MemoryBytes: gib, Create: true}
	with := func(f func(*Request)) Request { r := small; f(&r); return r }
	images := oneCPU8G
	images.Images = []string{"nginx", "ghcr.io/me/*"}
	noPorts := oneCPU8G
	noPorts.PortMin, noPorts.PortMax = 0, 0
	tests := []struct {
		name     string
		a        Allowance
		used     Usage
		req      Request
		resource string // "" fits
	}{
		{"fits", oneCPU8G, Usage{}, small, ""},
		{"fits exactly", oneCPU8G, Usage{CPUsMilli: 750, MemoryBytes: 7 * gib, Containers: 4}, small, ""},
		{"over by memory", oneCPU8G, Usage{MemoryBytes: 7*gib + 1}, small, "memory"},
		{"over by cpu", oneCPU8G, Usage{CPUsMilli: 751}, small, "cpus"},
		{"memory that overflows the sum", oneCPU8G, Usage{MemoryBytes: gib}, with(func(r *Request) { r.MemoryBytes = math.MaxInt64 }), "memory"},
		{"cpus that overflow the sum", oneCPU8G, Usage{CPUsMilli: 1}, with(func(r *Request) { r.CPUsMilli = math.MaxInt64 }), "cpus"},
		{"over by container count", oneCPU8G, Usage{Containers: 5}, small, "containers"},
		{"no memory limit", oneCPU8G, Usage{}, with(func(r *Request) { r.MemoryBytes = 0 }), "memory"},
		{"no cpu limit", oneCPU8G, Usage{}, with(func(r *Request) { r.CPUsMilli = 0 }), "cpus"},
		{"port in range", oneCPU8G, Usage{}, with(func(r *Request) { r.Ports = []int{8000, 8009} }), ""},
		{"port below the range", oneCPU8G, Usage{}, with(func(r *Request) { r.Ports = []int{7999} }), "ports"},
		{"port above the range", oneCPU8G, Usage{}, with(func(r *Request) { r.Ports = []int{8000, 8010} }), "ports"},
		{"no ports allowed", noPorts, Usage{}, with(func(r *Request) { r.Ports = []int{8000} }), "ports"},
		{"disk used up refuses a create", oneCPU8G, Usage{DiskBytes: 20 * gib}, small, "disk"},
		{"disk used up doesn't refuse a start", oneCPU8G, Usage{DiskBytes: 30 * gib}, with(func(r *Request) { r.Create = false }), ""},
		{"image by repository", images, Usage{}, small, ""},
		{"image by pattern", images, Usage{}, with(func(r *Request) { r.Image = "ghcr.io/me/app:2@sha256:abc" }), ""},
		{"image not allowed", images, Usage{}, with(func(r *Request) { r.Image = "ghcr.io/other/app:2" }), "image"},
		{"image lookalike", images, Usage{}, with(func(r *Request) { r.Image = "nginx-evil:1" }), "image"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Check(tt.a, tt.used, tt.req)
			var ex *ExceededError
			switch {
			case tt.resource == "" && err != nil:
				t.Fatalf("Check = %v, want nil", err)
			case tt.resource == "":
			case !errors.As(err, &ex):
				t.Fatalf("Check = %v, want an ExceededError", err)
			case ex.Resource != tt.resource:
				t.Fatalf("Check = %s (%v), want %s", ex.Resource, err, tt.resource)
			}
		})
	}
}

func TestTally(t *testing.T) {
	t.Parallel()
	got := Tally(oneCPU8G, []Container{{CPUsMilli: 500, MemoryBytes: gib}, {}}, 42)
	// The container without limits counts as the whole allowance.
	want := Usage{CPUsMilli: 1500, MemoryBytes: 9 * gib, DiskBytes: 42, Containers: 2}
	if got != want {
		t.Errorf("Tally = %+v, want %+v", got, want)
	}
	if left := Left(oneCPU8G, got); left != (Usage{DiskBytes: 20*gib - 42, Containers: 3}) {
		t.Errorf("Left = %+v", left)
	}
}

func TestFreePort(t *testing.T) {
	t.Parallel()
	a := Allowance{PortMin: 8000, PortMax: 8002}
	if p, ok := FreePort(a, map[int]bool{8000: true}); !ok || p != 8001 {
		t.Errorf("FreePort = %d, %v", p, ok)
	}
	if _, ok := FreePort(a, map[int]bool{8000: true, 8001: true, 8002: true}); ok {
		t.Error("FreePort found a port in a full range")
	}
	if _, ok := FreePort(Allowance{}, nil); ok {
		t.Error("FreePort found a port without a range")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		a     Allowance
		field string // "" valid
	}{
		{"valid", oneCPU8G, ""},
		{"no ports", Allowance{}, ""},
		{"negative memory", Allowance{MemoryBytes: -1}, "memory_bytes"},
		{"max below min", Allowance{PortMin: 9000, PortMax: 8000}, "port_max"},
		{"max without min", Allowance{PortMax: 8000}, "port_min"},
		{"port too high", Allowance{PortMin: 8000, PortMax: 70000}, "port_max"},
		{"bad pattern", Allowance{Images: []string{"nginx["}}, "images.0"},
		{"empty pattern", Allowance{Images: []string{""}}, "images.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			errs := tt.a.Validate()
			if tt.field == "" {
				if errs != nil {
					t.Fatalf("Validate = %v", errs)
				}
				return
			}
			if _, ok := errs[tt.field]; !ok {
				t.Fatalf("Validate = %v, want %s", errs, tt.field)
			}
		})
	}
}

func newTestService(t *testing.T) (*Service, int64, int64) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	res, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at, updated_at) VALUES ('bob', 'x', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	user, _ := res.LastInsertId()
	if res, err = db.ExecContext(ctx, `INSERT INTO environments (name, kind, created_at) VALUES ('local', 'local', 0)`); err != nil {
		t.Fatal(err)
	}
	env, _ := res.LastInsertId()
	now := time.UnixMilli(1_700_000_000_000)
	return New(Options{DB: db, Now: func() time.Time { return now }}), user, env
}

func TestStore(t *testing.T) {
	t.Parallel()
	s, user, env := newTestService(t)
	ctx := context.Background()
	if _, err := s.Get(ctx, user, env); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get before Set = %v", err)
	}
	a := oneCPU8G
	a.Images = []string{"nginx"}
	set, err := s.Set(ctx, user, env, a)
	if err != nil {
		t.Fatal(err)
	}
	if set.UpdatedAt.UnixMilli() != 1_700_000_000_000 {
		t.Errorf("UpdatedAt = %v", set.UpdatedAt)
	}
	got, err := s.Get(ctx, user, env)
	if err != nil || !reflect.DeepEqual(got, set) {
		t.Fatalf("Get = %+v, %v, want %+v", got, err, set)
	}
	if _, err := s.Set(ctx, user, env, Allowance{PortMax: 1}); err == nil {
		t.Error("Set took an invalid allowance")
	}
	if _, err := s.Set(ctx, user, env+1, a); err == nil {
		t.Error("Set took a missing environment")
	}
	if err := s.Delete(ctx, user, env); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, user, env); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v", err)
	}

	for _, img := range []string{"sha256:b", "sha256:a", "sha256:b"} {
		if err := s.RecordPull(ctx, user, env, img); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ForgetPull(ctx, user, env, "sha256:b"); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.Pulls(ctx, user, env); err != nil || !reflect.DeepEqual(ids, []string{"sha256:a"}) {
		t.Errorf("Pulls = %v, %v", ids, err)
	}
}

func TestLock(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestService(t)
	ctx := context.Background()
	unlock, err := s.Lock(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Another user, or the same user elsewhere, isn't held up.
	for _, k := range [][2]int64{{2, 1}, {1, 2}} {
		u, err := s.Lock(ctx, k[0], k[1])
		if err != nil {
			t.Fatal(err)
		}
		u()
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := s.Lock(cctx, 1, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock while held = %v", err)
	}

	got := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		u, err := s.Lock(ctx, 1, 1)
		if err != nil {
			t.Error(err)
			return
		}
		close(got)
		u()
	})
	select {
	case <-got:
		t.Fatal("second Lock didn't wait")
	case <-time.After(10 * time.Millisecond):
	}
	unlock()
	unlock() // a second call does nothing
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.locks) != 0 {
		t.Errorf("%d locks left", len(s.locks))
	}
}
