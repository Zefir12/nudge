// Package runqueue runs work one item per key at a time, with at most one item queued
// behind the running one. What happens when a request arrives while one is queued (it
// merges into the queued one, or replaces it) is the caller's policy.
package runqueue

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed means the Queue is shutting down.
var ErrClosed = errors.New("shutting down")

// Combine decides, when incoming arrives for a key that already has queued waiting,
// which of the two stays queued; the other is left out. It may change either before
// deciding. It is called with the Queue's lock held, so it must be quick and must not
// call the Queue.
type Combine[T any] func(queued, incoming T) T

// slot is a key's running item and the one queued after it.
type slot[T comparable] struct {
	running T
	next    T
}

// Queue runs the items it is given with run, one per key at a time. Items are compared
// by ==, so T is normally a pointer; its zero value means "none".
type Queue[K, T comparable] struct {
	ctx context.Context
	run func(T)

	mu     sync.Mutex
	closed bool
	// slots are the keys with an item under way, so bounded by the items' callers; a
	// slot goes when its items end.
	slots map[K]*slot[T]
	wg    sync.WaitGroup
}

// New returns a Queue that runs each item with run, on a goroutine the Queue owns.
// Once ctx ends the Queue takes no more items; run should then end the items it gets
// at once (Close waits for them).
func New[K, T comparable](ctx context.Context, run func(T)) *Queue[K, T] {
	return &Queue[K, T]{ctx: ctx, run: run, slots: map[K]*slot[T]{}}
}

// Closed reports whether the Queue takes no more items.
func (q *Queue[K, T]) Closed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closedLocked()
}

func (q *Queue[K, T]) closedLocked() bool { return q.closed || q.ctx.Err() != nil }

// Queued returns the item queued for key, if there is one.
func (q *Queue[K, T]) Queued(key K) (T, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var zero T
	if sl := q.slots[key]; sl != nil && sl.next != zero {
		return sl.next, true
	}
	return zero, false
}

// Len returns the number of keys with an item under way.
func (q *Queue[K, T]) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.slots)
}

// Submit gives item to key's queue: it runs now if nothing runs for key, else it waits
// behind the running item. If an item waits already, combine picks the one that stays
// queued. kept is the item that carries the request (item, or the queued one it went
// into); dropped is the one left out (item or the formerly queued one), or the zero
// value. The caller ends a dropped item itself: it never runs. After Close, or once
// the Queue's context ended, Submit takes nothing and returns ErrClosed.
func (q *Queue[K, T]) Submit(key K, item T, combine Combine[T]) (kept, dropped T, err error) {
	var zero T
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closedLocked() {
		return zero, zero, ErrClosed
	}
	sl := q.slots[key]
	switch {
	case sl == nil:
		sl = &slot[T]{running: item}
		q.slots[key] = sl
		q.wg.Go(func() { q.loop(key, sl, item) })
		return item, zero, nil
	case sl.next == zero:
		sl.next = item
		return item, zero, nil
	}
	kept = combine(sl.next, item)
	if kept == item {
		dropped, sl.next = sl.next, item
	} else {
		dropped = item
	}
	return kept, dropped, nil
}

// loop runs item and then the items queued meanwhile, until none is left.
func (q *Queue[K, T]) loop(key K, sl *slot[T], item T) {
	var zero T
	for item != zero {
		q.run(item)
		q.mu.Lock()
		item, sl.next = sl.next, zero
		sl.running = item
		if item == zero {
			delete(q.slots, key)
		}
		q.mu.Unlock()
	}
}

// Close takes no more items and waits for the ones under way and queued to be run. It
// doesn't end them: cancel the Queue's context first for that.
func (q *Queue[K, T]) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.wg.Wait()
}
