package api

import (
	"net/http"
	"sync"
)

const (
	// defaultMaxStreams is how many log, stats and exec streams (and stats overviews)
	// a user may have open at once in one environment.
	defaultMaxStreams = 32
	// adminStreamFactor multiplies the bound for admins, who watch every container.
	adminStreamFactor = 4
	errTooManyStreams = "too many log, stats and exec streams open in this environment"
)

// streamKey is one user's streams in one environment.
type streamKey struct{ user, env int64 }

// streamLimiter bounds the streams each user holds per environment. A key lives in the
// map only while it has a stream open, so the map is bounded by the open streams.
type streamLimiter struct {
	max  int
	mu   sync.Mutex
	open map[streamKey]int
}

func newStreamLimiter(bound int) *streamLimiter {
	if bound <= 0 {
		bound = defaultMaxStreams
	}
	return &streamLimiter{max: bound, open: map[streamKey]int{}}
}

// acquire takes a slot for user in env, or reports false when they hold their bound.
// release gives the slot back; calling it more than once is safe.
func (l *streamLimiter) acquire(user, env int64, admin bool) (release func(), ok bool) {
	bound := l.max
	if admin {
		bound *= adminStreamFactor
	}
	key := streamKey{user, env}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[key] >= bound {
		return nil, false
	}
	l.open[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.open[key]--; l.open[key] <= 0 {
				delete(l.open, key)
			}
		})
	}, true
}

// takeStream takes a stream slot for the request's user in env, answering 429 when they
// hold their bound. Take it before the upgrade (or the exec) and defer release.
func (s *server) takeStream(w http.ResponseWriter, r *http.Request, env int64) (release func(), ok bool) {
	release, ok = s.streamSlot(r, env)
	if !ok {
		s.writeError(w, http.StatusTooManyRequests, errTooManyStreams)
	}
	return release, ok
}

// streamSlot is takeStream without the answer. A request without a user (none reach
// these routes) shares the slots of user 0.
func (s *server) streamSlot(r *http.Request, env int64) (release func(), ok bool) {
	u, found := userFrom(r.Context())
	return s.streams.acquire(u.ID, env, found && isAdmin(u))
}
