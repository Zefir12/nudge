package events

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

// fakeSource hands out one stream per Events call; the test sends on msgs and errs.
type fakeSource struct {
	opened chan context.Context
	msgs   chan events.Message
	errs   chan error
}

func newFakeSource() *fakeSource {
	return &fakeSource{opened: make(chan context.Context, 4), msgs: make(chan events.Message), errs: make(chan error, 1)}
}

func (f *fakeSource) Events(ctx context.Context, _ client.EventsListOptions) client.EventsResult {
	f.opened <- ctx
	return client.EventsResult{Messages: f.msgs, Err: f.errs}
}

func (f *fakeSource) waitOpened(t *testing.T) context.Context {
	t.Helper()
	select {
	case ctx := <-f.opened:
		return ctx
	case <-time.After(5 * time.Second):
		t.Fatal("event stream not opened")
		return nil
	}
}

func newHub(buffer int) *Hub {
	return New(Options{Log: slog.New(slog.DiscardHandler), Buffer: buffer, MaxSubscribers: 2})
}

func msg(action string) events.Message {
	return events.Message{
		Type: events.ContainerEventType, Action: events.Action(action), TimeNano: 1_700_000_000_000_000_001,
		Actor: events.Actor{ID: "abc", Attributes: map[string]string{"name": "web", "image": "nginx"}},
	}
}

func recv(t *testing.T, s *Subscription) (Event, bool) {
	t.Helper()
	select {
	case e, ok := <-s.C():
		return e, ok
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
		return Event{}, false
	}
}

func TestFanOut(t *testing.T) {
	t.Parallel()
	h, src := newHub(4), newFakeSource()
	a, err := h.Subscribe(1, src)
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.Subscribe(1, src)
	if err != nil {
		t.Fatal(err)
	}
	ctx := src.waitOpened(t)
	src.msgs <- msg("start")
	src.msgs <- msg("exec_start: sh -c 'echo $SECRET'")
	for _, s := range []*Subscription{a, b} {
		e, _ := recv(t, s)
		want := Event{Time: time.Unix(0, 1_700_000_000_000_000_001).UTC(), Type: "container", Action: "start", ID: "abc", Name: "web"}
		if e.Time != want.Time || e.Type != want.Type || e.Action != want.Action || e.ID != want.ID || e.Name != want.Name || e.Attributes["image"] != "nginx" {
			t.Errorf("event = %+v, want %+v", e, want)
		}
		if e, _ := recv(t, s); e.Action != "exec_start" {
			t.Errorf("exec action = %q, want the command left out", e.Action)
		}
	}
	if _, err := h.Subscribe(1, src); !errors.Is(err, ErrTooMany) {
		t.Errorf("third subscribe: err = %v, want ErrTooMany", err)
	}
	if len(src.opened) != 0 {
		t.Error("a second subscriber opened another stream")
	}

	a.Close()
	a.Close()
	if ctx.Err() != nil {
		t.Fatal("stream closed while a subscriber is left")
	}
	b.Close()
	if ctx.Err() == nil || h.Feeds() != 0 {
		t.Errorf("stream open after the last subscriber left (feeds %d)", h.Feeds())
	}
	if _, ok := <-b.C(); ok || b.Err() != nil {
		t.Errorf("closed subscription: open or err %v", b.Err())
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	t.Parallel()
	h, src := newHub(1), newFakeSource()
	slow, _ := h.Subscribe(1, src)
	fast, _ := h.Subscribe(1, src)
	src.waitOpened(t)
	src.msgs <- msg("start")
	recv(t, fast)
	src.msgs <- msg("stop")
	recv(t, fast)

	recv(t, slow) // the first event stayed in its buffer; the second did not fit
	if _, ok := recv(t, slow); ok {
		t.Fatal("slow subscriber not dropped")
	}
	if !errors.Is(slow.Err(), ErrSlow) {
		t.Errorf("err = %v, want ErrSlow", slow.Err())
	}
	src.msgs <- msg("die")
	if e, _ := recv(t, fast); e.Action != "die" {
		t.Errorf("fast subscriber got %q after the drop", e.Action)
	}
	fast.Close()
}

func TestStreamEnds(t *testing.T) {
	t.Parallel()
	h, src := newHub(4), newFakeSource()
	a, _ := h.Subscribe(1, src)
	src.waitOpened(t)
	src.errs <- errors.New("daemon gone")
	if _, ok := recv(t, a); ok {
		t.Fatal("subscriber open after the stream ended")
	}
	if !errors.Is(a.Err(), ErrStream) {
		t.Errorf("err = %v, want ErrStream", a.Err())
	}
	a.Close()

	// The next subscriber opens a new stream.
	b, _ := h.Subscribe(1, src)
	defer b.Close()
	src.waitOpened(t)
	if h.Feeds() != 1 {
		t.Errorf("feeds = %d, want 1", h.Feeds())
	}
}

func TestShutdownEndsSubscriptions(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	h := New(Options{Log: slog.New(slog.DiscardHandler), Context: ctx})
	src := newFakeSource()
	a, _ := h.Subscribe(1, src)
	streamCtx := src.waitOpened(t)
	cancel()
	if _, ok := recv(t, a); ok {
		t.Fatal("subscriber open after shutdown")
	}
	if !errors.Is(a.Err(), ErrStream) || streamCtx.Err() == nil || h.Feeds() != 0 {
		t.Errorf("after shutdown: err %v, stream ctx %v, feeds %d", a.Err(), streamCtx.Err(), h.Feeds())
	}
	a.Close()
}

func TestReservedSubscription(t *testing.T) {
	t.Parallel()
	h, src := newHub(4), newFakeSource()
	for range 2 {
		if _, err := h.Subscribe(1, src); err != nil {
			t.Fatal(err)
		}
	}
	src.waitOpened(t)
	if _, err := h.Subscribe(1, src); !errors.Is(err, ErrTooMany) {
		t.Errorf("subscribe over the limit: err = %v, want ErrTooMany", err)
	}
	r, err := h.SubscribeReserved(1, src)
	if err != nil {
		t.Fatalf("reserved subscribe with the clients at the limit: %v", err)
	}
	if _, err := h.SubscribeReserved(1, src); !errors.Is(err, ErrTooMany) {
		t.Errorf("second reserved subscribe: err = %v, want ErrTooMany", err)
	}
	src.msgs <- msg("start")
	if e, _ := recv(t, r); e.Action != "start" {
		t.Errorf("reserved got %+v", e)
	}
	r.Close()
	if _, err := h.SubscribeReserved(1, src); err != nil {
		t.Errorf("reserved subscribe after Close: %v", err)
	}
}
