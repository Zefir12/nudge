package runqueue

import (
	"context"
	"errors"
	"testing"
	"time"
)

// item is a test run: run reports it started on started and ends when release closes.
type item struct {
	name    string
	release chan struct{}
}

func newItem(name string) *item { return &item{name: name, release: make(chan struct{})} }

// runner records the items run, in order.
type runner struct {
	started chan *item
}

func newRunner() *runner { return &runner{started: make(chan *item, 16)} }

func (r *runner) run(it *item) {
	r.started <- it
	<-it.release
}

func (r *runner) next(t *testing.T) *item {
	t.Helper()
	select {
	case it := <-r.started:
		return it
	case <-time.After(5 * time.Second):
		t.Fatal("no item started")
		return nil
	}
}

func (r *runner) none(t *testing.T) {
	t.Helper()
	select {
	case it := <-r.started:
		t.Fatalf("item %s started, want none", it.name)
	case <-time.After(20 * time.Millisecond):
	}
}

func keepQueued(queued, _ *item) *item     { return queued }
func takeIncoming(_, incoming *item) *item { return incoming }
func mustNotCombine(_, _ *item) *item      { panic("combine called") }
func submit(t *testing.T, q *Queue[string, *item], key string, it *item, c Combine[*item]) (kept, dropped *item) {
	t.Helper()
	kept, dropped, err := q.Submit(key, it, c)
	if err != nil {
		t.Fatalf("Submit(%s, %s): %v", key, it.name, err)
	}
	return kept, dropped
}

func TestCombine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		combine     Combine[*item]
		wantKept    string
		wantDropped string
	}{
		{"merge into queued", keepQueued, "second", "third"},
		{"replace queued", takeIncoming, "third", "second"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newRunner()
			q := New[string](context.Background(), r.run)
			first, second, third := newItem("first"), newItem("second"), newItem("third")

			if kept, dropped := submit(t, q, "a", first, mustNotCombine); kept != first || dropped != nil {
				t.Fatalf("first: kept %v, dropped %v; want it running", kept, dropped)
			}
			if r.next(t) != first {
				t.Fatal("first didn't start")
			}
			if kept, dropped := submit(t, q, "a", second, mustNotCombine); kept != second || dropped != nil {
				t.Fatalf("second: kept %v, dropped %v; want it queued", kept, dropped)
			}
			if got, ok := q.Queued("a"); !ok || got != second {
				t.Fatalf("Queued = %v, %v; want second", got, ok)
			}
			kept, dropped := submit(t, q, "a", third, tt.combine)
			if kept.name != tt.wantKept || dropped == nil || dropped.name != tt.wantDropped {
				t.Fatalf("third: kept %v, dropped %v; want %s, %s", kept, dropped, tt.wantKept, tt.wantDropped)
			}
			r.none(t)

			close(first.release)
			if got := r.next(t); got != kept {
				t.Fatalf("after first, %s started; want %s", got.name, kept.name)
			}
			close(kept.release)
			q.Close()
			r.none(t)
			if n := q.Len(); n != 0 {
				t.Errorf("Len = %d after the runs, want 0", n)
			}
			if _, ok := q.Queued("a"); ok {
				t.Error("Queued after the runs, want none")
			}
		})
	}
}

func TestKeysRunApart(t *testing.T) {
	t.Parallel()
	r := newRunner()
	q := New[string](context.Background(), r.run)
	a, b := newItem("a"), newItem("b")
	submit(t, q, "a", a, mustNotCombine)
	submit(t, q, "b", b, mustNotCombine)
	got := map[*item]bool{r.next(t): true, r.next(t): true}
	if !got[a] || !got[b] {
		t.Fatalf("started %v, want a and b at once", got)
	}
	if n := q.Len(); n != 2 {
		t.Errorf("Len = %d, want 2", n)
	}
	close(a.release)
	close(b.release)
	q.Close()

	if _, _, err := q.Submit("a", newItem("late"), mustNotCombine); !errors.Is(err, ErrClosed) {
		t.Errorf("Submit after Close: err = %v, want ErrClosed", err)
	}
}

func TestRestartAfterIdle(t *testing.T) {
	t.Parallel()
	r := newRunner()
	q := New[string](context.Background(), r.run)
	defer q.Close()
	for _, name := range []string{"one", "two"} {
		it := newItem(name)
		submit(t, q, "a", it, mustNotCombine)
		if r.next(t) != it {
			t.Fatalf("%s didn't start", name)
		}
		close(it.release)
		for deadline := time.Now().Add(5 * time.Second); q.Len() != 0; {
			if time.Now().After(deadline) {
				t.Fatal("slot kept after its run")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestClose(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	r := newRunner()
	q := New[string](ctx, r.run)
	running, queued := newItem("running"), newItem("queued")
	submit(t, q, "a", running, mustNotCombine)
	r.next(t)
	submit(t, q, "a", queued, mustNotCombine)

	cancel()
	if !q.Closed() {
		t.Error("Closed = false after the context ended")
	}
	if _, _, err := q.Submit("b", newItem("late"), mustNotCombine); !errors.Is(err, ErrClosed) {
		t.Errorf("Submit after the context ended: err = %v, want ErrClosed", err)
	}

	closed := make(chan struct{})
	go func() {
		q.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned with a run under way")
	case <-time.After(20 * time.Millisecond):
	}
	close(running.release)
	// The queued item is still run, so its owner can end it.
	if r.next(t) != queued {
		t.Fatal("queued item not run at Close")
	}
	close(queued.release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close didn't return")
	}
}
