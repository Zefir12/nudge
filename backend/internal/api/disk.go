package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/hostdisk"
	"git.zefirlabs.net/zefir/nudge/backend/internal/metrics"
	"git.zefirlabs.net/zefir/nudge/backend/internal/usage"
)

// diskSpaceTimeout bounds asking an agent for its disk's size.
const diskSpaceTimeout = 5 * time.Second

// diskResponse is an environment's disk: Docker's usage by kind from the last
// measurement (null before the first) and the size of the disk under Docker's root dir
// (null when unknown: no mount, an older agent, offline).
type diskResponse struct {
	Usage *metrics.Disk   `json:"usage"`
	Disk  *hostdisk.Space `json:"disk"`
}

// environmentDisk answers the environment's disk usage (admins only: it covers every
// user's objects).
func (s *server) environmentDisk(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	var resp diskResponse
	if s.metrics != nil {
		d, found, err := s.metrics.Disk(r.Context(), env.ID)
		if err != nil {
			s.internalError(w, "disk usage", err)
			return
		}
		if found {
			d.MeasuredAt = d.MeasuredAt.UTC()
			resp.Usage = &d
		}
	}
	resp.Disk = s.space(r.Context(), env)
	s.writeJSON(w, http.StatusOK, resp)
}

// space is the size of env's disk, nil when unknown.
func (s *server) space(ctx context.Context, env environments.Environment) *hostdisk.Space {
	if s.diskSpace == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, diskSpaceTimeout)
	defer cancel()
	sp, err := s.diskSpace(ctx, env)
	if err != nil {
		if !errors.Is(err, hostdisk.ErrUnknown) && ctx.Err() == nil {
			s.log.Debug("disk size", "env", env.ID, "err", err)
		}
		return nil
	}
	return &sp
}

// measureDisk measures the environment's disk usage now, like measureVolumes, but at most
// once a minute (429 sooner).
func (s *server) measureDisk(w http.ResponseWriter, r *http.Request) {
	s.measureNow(w, r, "measure disk usage", (*usage.Service).MeasureNow)
}

// measureNow runs a measurement of the environment and answers 204 once it's stored;
// 409 while one runs, 429 when measure refuses one this soon. The measurement outlives
// the request (a proxy may cut a long one) and ends only on its own timeout or at
// shutdown.
func (s *server) measureNow(w http.ResponseWriter, r *http.Request, msg string, measure func(*usage.Service, context.Context, environments.Environment) error) {
	if s.usage == nil {
		s.writeError(w, http.StatusServiceUnavailable, "disk usage isn't available")
		return
	}
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	stop := context.AfterFunc(s.done, cancel)
	defer stop()
	err := measure(s.usage, ctx, env)
	switch {
	case errors.Is(err, usage.ErrRunning):
		s.writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, usage.ErrTooSoon):
		w.Header().Set("Retry-After", strconv.Itoa(int(usage.MinGap/time.Second)))
		s.writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, usage.ErrSave):
		s.internalError(w, msg, err)
	case err != nil:
		s.dockerError(w, r, env, msg, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
