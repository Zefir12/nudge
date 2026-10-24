package audit

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

func newTestService(t *testing.T) (*Service, *fakeClock) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	return New(Options{DB: st.DB(), Now: clock.Now, Retention: 24 * time.Hour}), clock
}

func ids(entries []Entry) []int64 {
	out := make([]int64, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

func TestList(t *testing.T) {
	t.Parallel()
	s, clock := newTestService(t)
	ctx := context.Background()
	start := clock.Now()
	records := []Entry{
		{Username: "admin", Environment: "local", Action: "POST /api/v1/environments/{env}/containers/{id}/start", Target: "abc", Status: 204},
		{Username: "admin", Token: "ci", Environment: "local", Action: "POST /api/v1/environments/{env}/images/pull", Target: "nginx:1.29", Status: 200},
		{Username: "bob", Environment: "remote", Action: "DELETE /api/v1/environments/{env}/volumes/{id}", Target: "data_1", Status: 409},
		{Action: "POST /api/v1/setup", Target: "admin", Status: 201},
	}
	for _, e := range records {
		if err := s.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Minute)
	}
	yes, no := true, false
	tests := []struct {
		name string
		q    Query
		want []int64
	}{
		{"all, newest first", Query{}, []int64{4, 3, 2, 1}},
		{"page", Query{Limit: 2}, []int64{4, 3}},
		{"next page", Query{Before: 3, Limit: 2}, []int64{2, 1}},
		{"user, any case", Query{Username: "ADMIN"}, []int64{2, 1}},
		{"environment", Query{Environment: "remote"}, []int64{3}},
		{"search action", Query{Search: "IMAGES/PULL"}, []int64{2}},
		{"search target", Query{Search: "data_"}, []int64{3}},
		{"like wildcards are literal", Query{Search: "%"}, []int64{}},
		{"failed", Query{Failed: &yes}, []int64{3}},
		{"succeeded", Query{Failed: &no}, []int64{4, 2, 1}},
		{"since", Query{Since: start.Add(2 * time.Minute)}, []int64{4, 3}},
		{"until", Query{Until: start.Add(time.Minute)}, []int64{1}},
		{"combined", Query{Username: "admin", Failed: &no, Before: 2}, []int64{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := s.List(ctx, tt.q)
			if err != nil {
				t.Fatal(err)
			}
			if g := ids(got); !slices.Equal(g, tt.want) {
				t.Errorf("ids = %v, want %v", g, tt.want)
			}
		})
	}

	got, _, err := s.List(ctx, Query{Limit: 1, Before: 3})
	if err != nil {
		t.Fatal(err)
	}
	want := records[1]
	want.ID, want.Time = 2, start.Add(time.Minute)
	if len(got) != 1 || got[0] != want {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
}

func TestListLimit(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)
	ctx := context.Background()
	for range MaxLimit + 1 {
		if err := s.Record(ctx, Entry{Action: "POST /x", Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct{ limit, want int }{{0, DefaultLimit}, {-1, DefaultLimit}, {10, 10}, {MaxLimit + 1, MaxLimit}} {
		got, _, err := s.List(ctx, Query{Limit: tt.limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tt.want {
			t.Errorf("limit %d: got %d entries, want %d", tt.limit, len(got), tt.want)
		}
	}
}

func TestListMore(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)
	ctx := context.Background()
	for range 4 {
		if err := s.Record(ctx, Entry{Action: "POST /x", Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		q    Query
		want bool
	}{{Query{Limit: 3}, true}, {Query{Limit: 4}, false}, {Query{Limit: 2, Before: 3}, false}} {
		if _, more, err := s.List(ctx, tt.q); err != nil || more != tt.want {
			t.Errorf("%+v: more = %v, %v; want %v", tt.q, more, err, tt.want)
		}
	}
}

func TestPruneMaxEntries(t *testing.T) {
	t.Parallel()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(Options{DB: st.DB(), MaxEntries: 2})
	ctx := context.Background()
	for range 5 {
		if err := s.Record(ctx, Entry{Action: "POST /x", Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Prune(ctx); err != nil || n != 3 {
		t.Fatalf("Prune = %d, %v; want 3", n, err)
	}
	got, _, err := s.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if g := ids(got); !slices.Equal(g, []int64{5, 4}) {
		t.Errorf("left %v, want [5 4]", g)
	}
}

func TestRecordClipsFields(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)
	ctx := context.Background()
	long := strings.Repeat("é", maxField) // two bytes each
	if err := s.Record(ctx, Entry{Action: "POST /x", Target: long, Status: 201}); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got[0].Target); n != maxField {
		t.Errorf("target is %d bytes, want %d", n, maxField)
	}
}

func TestPrune(t *testing.T) {
	t.Parallel()
	s, clock := newTestService(t)
	ctx := context.Background()
	for range 2 {
		if err := s.Record(ctx, Entry{Action: "POST /x", Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(23 * time.Hour)
	if err := s.Record(ctx, Entry{Action: "POST /y", Status: 200}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Hour)
	n, err := s.Prune(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("pruned %d, want 2", n)
	}
	got, _, err := s.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if g := ids(got); !slices.Equal(g, []int64{3}) {
		t.Errorf("left %v, want [3]", g)
	}
}

func TestRunCleanupStops(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.RunCleanup(ctx, time.Millisecond)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunCleanup did not stop")
	}
}
