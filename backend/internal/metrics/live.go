package metrics

import (
	"cmp"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
)

const (
	// DefaultLiveInterval is the configuration's default sampling interval of a watched
	// environment.
	DefaultLiveInterval = 5 * time.Second
	// liveBuffer is how many ticks a live subscriber may lag behind before it is dropped.
	liveBuffer = 4
	// maxLiveSubscribers bounds the live subscriptions per environment.
	maxLiveSubscribers = 100
)

var (
	// ErrOff refuses a live subscription while collection is off.
	ErrOff = errors.New("metrics: collection is off")
	// ErrTooMany refuses a live subscription over the bound per environment.
	ErrTooMany = errors.New("metrics: too many live subscribers")
	// ErrSlow ends a live subscription whose buffer filled up: the client fell behind.
	ErrSlow = errors.New("metrics: live subscriber fell behind")
	// ErrStopped ends every live subscription of an environment that is no longer
	// collected: it was removed, or the server is shutting down.
	ErrStopped = errors.New("metrics: collection of the environment stopped")
)

// LiveTotal adds up a live tick's containers.
type LiveTotal struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryUsage uint64  `json:"memory_usage"`
	NetworkRx   uint64  `json:"network_rx"`
	NetworkTx   uint64  `json:"network_tx"`
	BlockRead   uint64  `json:"block_read"`
	BlockWrite  uint64  `json:"block_write"`
}

// Live is one tick of an environment as a live subscriber sees it: the containers in
// its scope sorted by name, their totals, and how many in its scope could not be
// sampled.
type Live struct {
	Time       time.Time   `json:"time"`
	Containers []Container `json:"containers"`
	Skipped    int         `json:"skipped"`
	Total      LiveTotal   `json:"total"`
}

// tick is one collected tick with every container's owner, so each subscriber gets its
// own share.
type tick struct {
	time    time.Time
	sampled []Container
	owners  map[string]string
	// skippedOwners are the owners of the containers that could not be sampled.
	skippedOwners []string
}

func newTick(at time.Time, items []container.Summary, sampled []Container, owners map[string]string) *tick {
	t := &tick{time: at, sampled: sampled, owners: owners}
	got := make(map[string]bool, len(sampled))
	for _, c := range sampled {
		got[c.ID] = true
	}
	for _, it := range items {
		if !got[it.ID] {
			t.skippedOwners = append(t.skippedOwners, owners[it.ID])
		}
	}
	return t
}

// in reports whether a container with owner is in sc.
func (sc Scope) in(owner string) bool {
	return sc.All || (sc.Owner != "" && owner == sc.Owner)
}

// view is t within sc.
func (t *tick) view(sc Scope) Live {
	out := Live{Time: t.time.UTC(), Containers: []Container{}}
	for _, c := range t.sampled {
		if !sc.in(t.owners[c.ID]) {
			continue
		}
		out.Containers = append(out.Containers, c)
		out.Total.CPUPercent += c.CPUPercent
		out.Total.MemoryUsage += c.MemoryUsage
		out.Total.NetworkRx += c.NetworkRx
		out.Total.NetworkTx += c.NetworkTx
		out.Total.BlockRead += c.BlockRead
		out.Total.BlockWrite += c.BlockWrite
	}
	for _, o := range t.skippedOwners {
		if sc.in(o) {
			out.Skipped++
		}
	}
	slices.SortFunc(out.Containers, func(a, b Container) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// liveHub fans each environment's ticks out to its live subscribers and tells the
// environment's collector when it is watched.
type liveHub struct {
	mu     sync.Mutex
	subs   map[int64]map[*LiveSubscription]bool
	wake   map[int64]chan struct{} // one per running collector
	last   map[int64]*tick         // each collected environment's latest tick
	closed bool
}

func newLiveHub() *liveHub {
	return &liveHub{
		subs: map[int64]map[*LiveSubscription]bool{},
		wake: map[int64]chan struct{}{},
		last: map[int64]*tick{},
	}
}

// LiveSubscription receives one environment's ticks, within its scope, until it ends.
type LiveSubscription struct {
	hub   *liveHub
	env   int64
	scope Scope
	c     chan Live
	err   error // set under hub.mu before c is closed
}

// SubscribeLive adds a live subscriber to env: while env has one, it is sampled every
// live interval. The latest tick, when it is recent, is delivered at once. The caller
// must Close the subscription.
func (s *Service) SubscribeLive(env int64, sc Scope) (*LiveSubscription, error) {
	if s.interval <= 0 {
		return nil, ErrOff
	}
	h := s.live
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrStopped
	}
	subs := h.subs[env]
	if len(subs) >= maxLiveSubscribers {
		return nil, ErrTooMany
	}
	if subs == nil {
		subs = map[*LiveSubscription]bool{}
		h.subs[env] = subs
		// The collector switches to the live interval at once.
		if w := h.wake[env]; w != nil {
			select {
			case w <- struct{}{}:
			default:
			}
		}
	}
	sub := &LiveSubscription{hub: h, env: env, scope: sc, c: make(chan Live, liveBuffer)}
	subs[sub] = true
	if t := h.last[env]; t != nil && s.clock.Now().Sub(t.time) <= staleTicks*s.interval {
		sub.c <- t.view(sc)
	}
	return sub, nil
}

// C delivers the ticks; it is closed when the subscription ends.
func (sub *LiveSubscription) C() <-chan Live { return sub.c }

// Err says why C was closed: ErrSlow, ErrStopped, or nil after Close.
func (sub *LiveSubscription) Err() error {
	sub.hub.mu.Lock()
	defer sub.hub.mu.Unlock()
	return sub.err
}

// Close ends the subscription; safe to call more than once.
func (sub *LiveSubscription) Close() {
	h := sub.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[sub.env][sub] {
		h.drop(sub, nil)
	}
}

// drop removes sub and closes it with err. The caller holds h.mu.
func (h *liveHub) drop(sub *LiveSubscription, err error) {
	subs := h.subs[sub.env]
	delete(subs, sub)
	if len(subs) == 0 {
		delete(h.subs, sub.env)
	}
	sub.err = err
	close(sub.c)
}

// register is called by env's collector: wake receives when env gets its first live
// subscriber. unregister ends env's subscriptions with ErrStopped.
func (h *liveHub) register(env int64) (wake <-chan struct{}, unregister func()) {
	w := make(chan struct{}, 1)
	h.mu.Lock()
	h.wake[env] = w
	h.mu.Unlock()
	return w, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.wake[env] == w {
			delete(h.wake, env)
			delete(h.last, env)
			for sub := range h.subs[env] {
				h.drop(sub, ErrStopped)
			}
		}
	}
}

// watched reports whether env has a live subscriber.
func (h *liveHub) watched(env int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[env]) > 0
}

// publish keeps t as env's latest tick and sends each subscriber its share; one whose
// buffer is full is dropped.
func (h *liveHub) publish(env int64, t *tick) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.wake[env] == nil {
		return // the collector is gone
	}
	h.last[env] = t
	for sub := range h.subs[env] {
		select {
		case sub.c <- t.view(sub.scope):
		default:
			h.drop(sub, ErrSlow)
		}
	}
}

// close ends every subscription with ErrStopped and refuses new ones (Run is done).
func (h *liveHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, subs := range h.subs {
		for sub := range subs {
			h.drop(sub, ErrStopped)
		}
	}
}
