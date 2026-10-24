package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/updates"
)

// updateCheckTimeout bounds a check started from the API; registries are slow and an
// environment can run many images.
const updateCheckTimeout = 5 * time.Minute

type updateJSON struct {
	Image        string         `json:"image"`
	ImageID      string         `json:"image_id"`
	Status       updates.Status `json:"status"`
	LocalDigest  string         `json:"local_digest"`
	RemoteDigest string         `json:"remote_digest"`
	Error        string         `json:"error"`
	CheckedAt    time.Time      `json:"checked_at"`
}

func toUpdatesJSON(results []updates.Result) []updateJSON {
	out := make([]updateJSON, 0, len(results))
	for _, r := range results {
		out = append(out, updateJSON(r))
	}
	return out
}

// listUpdates answers the environment's last update check results.
func (s *server) listUpdates(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		s.writeError(w, http.StatusServiceUnavailable, "update checks aren't available")
		return
	}
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	results, err := s.updates.List(r.Context(), env.ID)
	if err != nil {
		s.internalError(w, "list updates", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toUpdatesJSON(results))
}

// checkUpdates checks the environment's images now and answers the results. The check
// isn't cancelled when the client goes away, so its results are saved, only at shutdown.
func (s *server) checkUpdates(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		s.writeError(w, http.StatusServiceUnavailable, "update checks aren't available")
		return
	}
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), updateCheckTimeout)
	defer cancel()
	// Shutdown doesn't wait for the check.
	defer context.AfterFunc(s.done, cancel)()
	results, err := s.updates.Check(ctx, env)
	if errors.Is(err, updates.ErrRunning) {
		s.writeError(w, http.StatusConflict, "an update check is already running; try again when it's done")
		return
	}
	if errors.Is(err, updates.ErrStore) {
		s.internalError(w, "check updates", err)
		return
	}
	if err != nil {
		s.dockerError(w, r, env, "check updates", err)
		return
	}
	s.envs.Seen(r.Context(), &env)
	s.writeJSON(w, http.StatusOK, toUpdatesJSON(results))
}
