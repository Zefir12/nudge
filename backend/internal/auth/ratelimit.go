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

// reserve counts an attempt for key at now, unless key already reached the limit; then
// it reports false and how long the block lasts. An attempt that turns out fine is
// handed back with release (or reset).
func (l *limiter) reserve(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.entries[key]
	if ok && !now.Before(w.start.Add(l.window)) {
		delete(l.entries, key)
		ok = false
	}
	if !ok {
		if len(l.entries) >= l.max {
			l.pruneLocked(now)
			if len(l.entries) >= l.max {
				l.evictOldestLocked()
			}
		}
		w = &window{start: now}
		l.entries[key] = w
	}
	if w.failures >= l.limit {
		return w.start.Add(l.window).Sub(now), false
	}
	w.failures++
	return 0, true
}

// release hands back an attempt reserved at reservedAt, if its window still exists.
func (l *limiter) release(key string, reservedAt time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.entries[key]
	if !ok || w.start.After(reservedAt) || w.failures == 0 {
		return
	}
	w.failures--
	if w.failures == 0 {
		delete(l.entries, key)
	}
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

// Attempt counts a check of another credential from ip (a deploy webhook's secret)
// against ip's failed-login limit, so guessing secrets is as slow as guessing passwords.
// It fails with a *RateLimitedError while ip is blocked; otherwise the caller calls
// succeeded once the credential turned out valid, which hands the attempt back.
func (s *Service) Attempt(ip string) (succeeded func(), err error) {
	now := s.now()
	if d, ok := s.ipLimit.reserve(ip, now); !ok {
		return nil, &RateLimitedError{RetryAfter: d}
	}
	return func() { s.ipLimit.release(ip, now) }, nil
}
