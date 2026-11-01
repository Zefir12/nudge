// Package events fans out each environment's Docker event stream to the UI clients that
// watch it. An environment's stream is opened by its first subscriber and closed when the
// last one leaves, so the daemon sees one subscription however many clients watch.
package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

// Source is the part of a Docker client the hub reads events from.
type Source interface {
	Events(ctx context.Context, opts client.EventsListOptions) client.EventsResult
}

// Event is one Docker event as the API sends it.
type Event struct {
	Time   time.Time `json:"time"`
	Type   string    `json:"type"`
	Action string    `json:"action"`
	// ID is the object's ID: a container or image ID, a volume or network name.
	ID string `json:"id"`
	// Name is the object's name when the event carries one (containers, networks,
	// images by reference).
	Name       string            `json:"name,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

var (
	// ErrSlow ends a subscription whose buffer filled up: the client fell behind.
	ErrSlow = errors.New("events: subscriber fell behind")
	// ErrStream ends every subscription of an environment whose Docker stream ended.
	ErrStream = errors.New("events: docker event stream ended")
	// ErrTooMany refuses a subscription over Options.MaxSubscribers, or a second
	// reserved one of the same name.
	ErrTooMany = errors.New("events: too many subscribers")
)

// Options configure a Hub.
type Options struct {
	Log *slog.Logger
	// Context ends every feed and subscription when it is done (server shutdown); nil
	// means never.
	Context context.Context
	// Buffer is how many events a subscriber may lag behind before it is dropped.
	Buffer int
	// MaxSubscribers bounds the subscriptions per environment, not counting the
	// reserved ones.
	MaxSubscribers int
}

// Hub holds one feed per watched environment.
type Hub struct {
	ctx            context.Context
	log            *slog.Logger
	buffer         int
	maxSubscribers int

	mu    sync.Mutex
	feeds map[int64]*feed
}

// feed is one environment's Docker stream and its subscribers.
type feed struct {
	subs map[*Subscription]bool
	// reserved are the SubscribeReserved subscriptions by name.
	reserved map[string]*Subscription
	cancel   context.CancelFunc
}

// Subscription receives one environment's events until it ends.
type Subscription struct {
	hub  *Hub
	env  int64
	feed *feed
	// name is a reserved subscription's name, empty for the others.
	name string
	c    chan Event
	err  error // set under hub.mu before c is closed
}

// New returns an empty hub.
func New(opts Options) *Hub {
	if opts.Buffer <= 0 {
		opts.Buffer = 64
	}
	if opts.MaxSubscribers <= 0 {
		opts.MaxSubscribers = 100
	}
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	return &Hub{ctx: opts.Context, log: opts.Log, buffer: opts.Buffer, maxSubscribers: opts.MaxSubscribers, feeds: map[int64]*feed{}}
}

// Subscribe adds a subscriber to env's feed, opening the Docker stream from src when it
// is the first. The caller must Close the subscription.
func (h *Hub) Subscribe(env int64, src Source) (*Subscription, error) {
	return h.subscribe(env, src, "")
}

// SubscribeReserved is Subscribe for an internal reader of env (the notification
// watcher, the activity counter), named by name: it doesn't count against
// Options.MaxSubscribers, so UI clients can't crowd it out, and a second one of the same
// name while it is open fails with ErrTooMany.
func (h *Hub) SubscribeReserved(env int64, name string, src Source) (*Subscription, error) {
	if name == "" {
		return nil, errors.New("events: reserved subscription without a name")
	}
	return h.subscribe(env, src, name)
}

func (h *Hub) subscribe(env int64, src Source, name string) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f := h.feeds[env]
	if f == nil {
		ctx, cancel := context.WithCancel(h.ctx)
		f = &feed{subs: map[*Subscription]bool{}, reserved: map[string]*Subscription{}, cancel: cancel}
		h.feeds[env] = f
		// Events waits for the daemon's answer, so it runs outside the lock.
		go h.run(ctx, env, f, src)
	}
	if name != "" && f.reserved[name] != nil {
		return nil, ErrTooMany
	}
	if name == "" && len(f.subs)-len(f.reserved) >= h.maxSubscribers {
		return nil, ErrTooMany
	}
	s := &Subscription{hub: h, env: env, feed: f, name: name, c: make(chan Event, h.buffer)}
	f.subs[s] = true
	if name != "" {
		f.reserved[name] = s
	}
	return s, nil
}

// C delivers the events; it is closed when the subscription ends.
func (s *Subscription) C() <-chan Event { return s.c }

// Err says why C was closed: ErrSlow, ErrStream, or nil after Close.
func (s *Subscription) Err() error {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.err
}

// Close ends the subscription; the environment's Docker stream closes with its last one.
// It is safe to call more than once.
func (s *Subscription) Close() {
	h := s.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.feed.subs[s] {
		h.drop(s, nil)
	}
}

// drop removes s from its feed and closes it with err; the feed goes when it is empty.
// The caller holds h.mu.
func (h *Hub) drop(s *Subscription, err error) {
	f := s.feed
	delete(f.subs, s)
	if s.name != "" && f.reserved[s.name] == s {
		delete(f.reserved, s.name)
	}
	s.err = err
	close(s.c)
	if len(f.subs) == 0 && h.feeds[s.env] == f {
		delete(h.feeds, s.env)
		f.cancel()
	}
}

// run passes the Docker stream's events to f's subscribers until the stream ends or ctx
// is done: cancelled with the last subscriber, or at shutdown, which ends the others.
func (h *Hub) run(ctx context.Context, env int64, f *feed, src Source) {
	res := src.Events(ctx, client.EventsListOptions{})
	for {
		select {
		case <-ctx.Done():
			h.end(ctx, env, f, nil)
			return
		case m, ok := <-res.Messages:
			if !ok {
				h.end(ctx, env, f, nil)
				return
			}
			h.publish(f, toEvent(m))
		case err := <-res.Err:
			h.end(ctx, env, f, err)
			return
		}
	}
}

func (h *Hub) publish(f *feed, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range f.subs {
		select {
		case s.c <- e:
		default:
			h.drop(s, ErrSlow)
		}
	}
}

// end closes every subscriber of f after its Docker stream ended.
func (h *Hub) end(ctx context.Context, env int64, f *feed, err error) {
	if ctx.Err() == nil && err != nil && !errors.Is(err, io.EOF) {
		// The daemon's message stays in the log.
		h.log.Warn("docker events", "env", env, "err", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range f.subs {
		h.drop(s, ErrStream)
	}
	if h.feeds[env] == f {
		delete(h.feeds, env)
		f.cancel()
	}
}

// Feeds returns how many environments have an open Docker stream, for tests.
func (h *Hub) Feeds() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.feeds)
}

func toEvent(m events.Message) Event {
	e := Event{
		Type:   string(m.Type),
		Action: string(m.Action),
		ID:     m.Actor.ID,
		Name:   m.Actor.Attributes["name"],
	}
	if m.TimeNano != 0 {
		e.Time = time.Unix(0, m.TimeNano).UTC()
	} else {
		e.Time = time.Unix(m.Time, 0).UTC()
	}
	if len(m.Actor.Attributes) > 0 {
		e.Attributes = maps.Clone(m.Actor.Attributes)
	}
	// Exec events carry the command after the action ("exec_start: sh -c ..."); it can
	// hold secrets typed on the command line, so only the action is sent.
	if action, _, ok := strings.Cut(e.Action, ": "); ok && strings.HasPrefix(action, "exec_") {
		e.Action = action
	}
	return e
}
