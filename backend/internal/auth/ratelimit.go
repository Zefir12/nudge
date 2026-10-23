package auth

import (
	"sync"
	"time"
)

// limiter counts failed logins per key (an IP or a username) in a fixed window and
// blocks a key that reached its limit until the window ends. It holds at most max keys:
// when full it drops expired windows, then the oldest one.
type limiter struct {
	limit  int
	window time.Duration
	max    int

	mu      sync.Mutex
	entries map[string]*window
}

type window struct {
	start    time.Time
	failures int
}

func newLimiter(limit int, period time.Duration, maxKeys int) *limiter {
	return &limiter{limit: limit, window: period, max: maxKeys, entries: make(map[string]*window)}
}

// blocked reports whether key is blocked at now and, if so, for how much longer.
func (l *limiter) blocked(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.entries[key]
	if !ok {
		return 0, false
	}
	end := w.start.Add(l.window)
	if !now.Before(end) {
		delete(l.entries, key)
		return 0, false
	}
	if w.failures < l.limit {
		return 0, false
	}
	return end.Sub(now), true
}

// fail records a failed attempt for key.
func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.entries[key]
	if ok && !now.Before(w.start.Add(l.window)) {
		ok = false
	}
	if !ok {
		if _, exists := l.entries[key]; !exists && len(l.entries) >= l.max {
			l.pruneLocked(now)
			if len(l.entries) >= l.max {
				l.evictOldestLocked()
			}
		}
		w = &window{start: now}
		l.entries[key] = w
	}
	w.failures++
}

// reset forgets key, after a successful login.
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// prune drops every expired window.
func (l *limiter) prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
}

func (l *limiter) pruneLocked(now time.Time) {
	for k, w := range l.entries {
		if !now.Before(w.start.Add(l.window)) {
			delete(l.entries, k)
		}
	}
}

func (l *limiter) evictOldestLocked() {
	var oldest string
	var oldestStart time.Time
	found := false
	for k, w := range l.entries {
		if !found || w.start.Before(oldestStart) {
			oldest, oldestStart, found = k, w.start, true
		}
	}
	delete(l.entries, oldest)
}

func (l *limiter) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}
