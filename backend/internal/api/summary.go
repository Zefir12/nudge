package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

const (
	// summaryFresh is how long an environment's usage summary is served without sampling
	// again; the global dashboard asks for every environment's.
	summaryFresh = 30 * time.Second
	// summaryKeep drops summaries nobody refreshed for this long (deleted environments).
	summaryKeep = 10 * time.Minute
)

// usageSummary is an environment's usage totals from its last overview; Containers
// counts the containers sampled.
type usageSummary struct {
	SampledAt   time.Time `json:"sampled_at"`
	CPUPercent  float64   `json:"cpu_percent"`
	MemoryUsage uint64    `json:"memory_usage"`
	Containers  int       `json:"containers"`
}

// summaryEntry is one environment's cached summary; done is open while a sampling runs,
// and err is the last sampling's.
type summaryEntry struct {
	sum  usageSummary
	err  error
	done chan struct{}
}

// environmentSummary answers the environment's usage totals, sampled at most once per
// summaryFresh: concurrent callers share one sampling, and an overview refreshes it too.
func (s *server) environmentSummary(w http.ResponseWriter, r *http.Request) {
	resp, ok := s.runDocker(r.Context(), w, r, "environment stats", "", statsTimeout, func(ctx context.Context, c docker.Client) (any, error) {
		id, err := strconv.ParseInt(r.PathValue("env"), 10, 64)
		if err != nil {
			return nil, err
		}
		return s.summary(ctx, id, c)
	})
	if ok {
		s.writeJSON(w, http.StatusOK, resp)
	}
}

// summary returns the cached summary while it is fresh, else waits for a sampling. The
// sampling outlives a caller that leaves (others may wait for it) but not the server.
func (s *server) summary(ctx context.Context, env int64, c docker.Client) (usageSummary, error) {
	s.mu.Lock()
	e := s.summaries[env]
	if e == nil {
		e = &summaryEntry{}
		s.summaries[env] = e
	}
	if e.done == nil && !e.sum.SampledAt.IsZero() && s.now().Sub(e.sum.SampledAt) < summaryFresh {
		sum := e.sum
		s.mu.Unlock()
		return sum, nil
	}
	if e.done == nil {
		e.done = make(chan struct{})
		go s.refreshSummary(env, c, e)
	}
	done := e.done
	s.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return usageSummary{}, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return e.sum, e.err
}

// refreshSummary samples the environment for e and wakes its waiters.
func (s *server) refreshSummary(env int64, c docker.Client, e *summaryEntry) {
	ctx, cancel := context.WithTimeout(s.done, statsTimeout)
	defer cancel()
	stats, err := s.sampleEnvironment(ctx, c)
	if err == nil {
		err = incomplete(ctx, stats)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e.err = err
	if err == nil {
		s.keepSummaryLocked(env, stats)
	}
	close(e.done)
	e.done = nil
}

// incomplete refuses a sampling cut short (a timeout or shutdown) or one where no
// listed container could be sampled: its totals would be cached as if they were whole.
func incomplete(ctx context.Context, stats environmentStats) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(stats.Containers) == 0 && stats.Skipped > 0 {
		return errors.New("no container could be sampled")
	}
	return nil
}

// storeSummary keeps an overview's totals as the environment's summary.
func (s *server) storeSummary(env int64, stats environmentStats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keepSummaryLocked(env, stats)
}

// keepSummaryLocked stores the environment's summary and drops the ones nobody
// refreshed for summaryKeep. s.mu must be held.
func (s *server) keepSummaryLocked(env int64, stats environmentStats) {
	now := s.now()
	e := s.summaries[env]
	if e == nil {
		e = &summaryEntry{}
		s.summaries[env] = e
	}
	e.sum, e.err = summaryOf(stats, now), nil
	for id, old := range s.summaries {
		if old.done == nil && now.Sub(old.sum.SampledAt) > summaryKeep {
			delete(s.summaries, id)
		}
	}
}

func summaryOf(stats environmentStats, at time.Time) usageSummary {
	return usageSummary{
		SampledAt:   at.UTC(),
		CPUPercent:  stats.Total.CPUPercent,
		MemoryUsage: stats.Total.MemoryUsage,
		Containers:  len(stats.Containers),
	}
}
