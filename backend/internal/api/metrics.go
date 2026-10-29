package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/metrics"
)

// defaultSince is how far back a history goes when the request doesn't say.
const defaultSince = 24 * time.Hour

const (
	// errNoSample answers a summary without a recent sample.
	errNoSample = "no recent usage sample"
	// errCollectionOff answers a live stream while collection is off.
	errCollectionOff = "usage collection is off"
)

// environmentSummary answers the environment's usage totals (a user's: of their
// containers) from the collector's latest tick; 404 when there is none from the last
// few intervals (collection is off, or the environment is offline).
func (s *server) environmentSummary(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	sc, err := s.scopeOf(r.Context())
	if err != nil {
		s.internalError(w, "environment summary", err)
		return
	}
	if s.metrics == nil {
		s.writeError(w, http.StatusNotFound, errNoSample)
		return
	}
	sum, found, err := s.metrics.Latest(r.Context(), env.ID, metrics.Scope{All: sc.all, Owner: sc.key})
	if err != nil {
		s.internalError(w, "environment summary", err)
		return
	}
	if !found {
		s.writeError(w, http.StatusNotFound, errNoSample)
		return
	}
	s.writeJSON(w, http.StatusOK, sum)
}

// environmentMetrics answers the environment's usage history: of every container for
// an admin, of their own containers for a user.
func (s *server) environmentMetrics(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	q, ok := s.metricsQuery(w, r)
	if !ok {
		return
	}
	sc, err := s.scopeOf(r.Context())
	if err != nil {
		s.internalError(w, "environment metrics", err)
		return
	}
	q.Env, q.Scope = env.ID, metrics.Scope{All: sc.all, Owner: sc.key}
	s.answerHistory(w, r, q)
}

// containerMetrics answers one container's usage history; the container must exist and
// be the user's, like on the other container routes.
func (s *server) containerMetrics(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "container")
	if !ok {
		return
	}
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	q, ok := s.metricsQuery(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dockerTimeout)
	ct, _, err := s.ownedContainer(ctx, c, id)
	cancel()
	if docker.NotFound(err) {
		s.writeError(w, http.StatusNotFound, "container not found")
		return
	}
	if err != nil {
		s.dockerError(w, r, env, "container metrics", err)
		return
	}
	s.envs.Seen(r.Context(), &env)
	q.Env, q.Container = env.ID, ct.ID
	s.answerHistory(w, r, q)
}

// metricsQuery reads since (a duration back, such as 24h, or an RFC 3339 time; 24h if
// absent) and resolution (a bucket size such as 15m; as stored if absent).
func (s *server) metricsQuery(w http.ResponseWriter, r *http.Request) (metrics.Query, bool) {
	q := metrics.Query{Since: s.now().Add(-defaultSince)}
	if v := r.URL.Query().Get("since"); v != "" {
		since, err := parseSince(v, s.now())
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "since: want a duration such as 24h or an RFC 3339 time")
			return metrics.Query{}, false
		}
		q.Since = since
	}
	if v := r.URL.Query().Get("resolution"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			s.writeError(w, http.StatusBadRequest, "resolution: want a duration of at least 1s, such as 15m")
			return metrics.Query{}, false
		}
		q.Step = d
	}
	return q, true
}

func parseSince(v string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(v); err == nil {
		if d <= 0 {
			return time.Time{}, errors.New("not a positive duration")
		}
		return now.Add(-d), nil
	}
	return time.Parse(time.RFC3339, v)
}

func (s *server) answerHistory(w http.ResponseWriter, r *http.Request, q metrics.Query) {
	if s.metrics == nil {
		s.writeJSON(w, http.StatusOK, metrics.History{Source: metrics.SourceRaw, Step: 1, Points: []metrics.Point{}})
		return
	}
	h, err := s.metrics.History(r.Context(), q)
	if err != nil {
		s.internalError(w, "metrics history", err)
		return
	}
	s.writeJSON(w, http.StatusOK, h)
}

// environmentLiveMetrics streams the environment's usage over a WebSocket, one frame per
// collector tick: every container for an admin, their own containers for a user. While
// anyone watches, the environment is sampled every live interval. The latest tick, if
// recent, comes at once. A client that falls behind is sent an error frame and
// disconnected, and so is every client when collection of the environment stops.
func (s *server) environmentLiveMetrics(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	sc, err := s.scopeOf(r.Context())
	if err != nil {
		s.internalError(w, "live metrics", err)
		return
	}
	if s.metrics == nil {
		s.writeError(w, http.StatusNotFound, errCollectionOff)
		return
	}
	sub, err := s.metrics.SubscribeLive(env.ID, metrics.Scope{All: sc.all, Owner: sc.key})
	switch {
	case errors.Is(err, metrics.ErrOff):
		s.writeError(w, http.StatusNotFound, errCollectionOff)
		return
	case errors.Is(err, metrics.ErrTooMany):
		s.writeError(w, http.StatusServiceUnavailable, "too many live usage streams open for this environment")
		return
	case err != nil:
		s.writeError(w, http.StatusServiceUnavailable, "usage collection is stopping")
		return
	}
	defer sub.Close()

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.log.Warn("live metrics: websocket upgrade", "env", env.ID, "err", err)
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ctx, cancel := context.WithCancel(ws.CloseRead(r.Context()))
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	wg.Go(func() { keepAlive(ctx, cancel, ws) })

	for {
		select {
		case <-ctx.Done():
			return
		case l, ok := <-sub.C():
			if !ok {
				msg := "usage collection of the environment stopped"
				if errors.Is(sub.Err(), metrics.ErrSlow) {
					msg = "the usage stream fell behind; reconnect to resume"
				}
				if wsjson.Write(ctx, ws, streamEnd{Error: msg}) == nil {
					_ = ws.Close(websocket.StatusNormalClosure, "")
				}
				return
			}
			if wsjson.Write(ctx, ws, l) != nil {
				return
			}
		}
	}
}
