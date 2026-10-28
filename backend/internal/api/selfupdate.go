package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/selfupdate"
	"git.zefirlabs.net/zefir/nudge/backend/internal/updates"
)

// selfUpdateJSON is the update state of nudge's own container: the server's, or an
// agent's on its environment.
type selfUpdateJSON struct {
	Version string `json:"version"`
	// Image is the reference the container runs, empty when it isn't found.
	Image string `json:"image,omitempty"`
	// Status is the last update check of Image: available, current or unknown (Error
	// says why).
	Status       updates.Status `json:"status"`
	LocalDigest  string         `json:"local_digest,omitempty"`
	RemoteDigest string         `json:"remote_digest,omitempty"`
	CheckedAt    *time.Time     `json:"checked_at,omitempty"`
	Error        string         `json:"error,omitempty"`
	// CanUpdate is false when nudge can't replace this container itself; Reason says why.
	CanUpdate bool   `json:"can_update"`
	Reason    string `json:"reason,omitempty"`
}

// selfUpdateStarted answers an update: Started is false when the pull brought nothing
// newer, so the container was left alone.
type selfUpdateStarted struct {
	Started bool `json:"started"`
}

// errNotAgent refuses the agent update endpoints on the local environment.
var errNotAgent = &statusError{code: http.StatusNotFound, msg: "not an agent environment; the server updates from Settings"}

// selfErrors are the reasons nudge can't update a container itself, safe to show.
var selfErrors = []error{
	selfupdate.ErrNoSelf, selfupdate.ErrNoSocket, selfupdate.ErrPinned, selfupdate.ErrAutoRemove,
	selfupdate.ErrRunning, selfupdate.ErrOldAgent,
}

func selfReason(err error) (string, bool) {
	for _, e := range selfErrors {
		if errors.Is(err, e) {
			return e.Error(), true
		}
	}
	return "", false
}

// localEnvironment returns the local environment, where the server's own container runs.
func (s *server) localEnvironment(ctx context.Context) (environments.Environment, docker.Client, error) {
	list, err := s.envs.List(ctx)
	if err != nil {
		return environments.Environment{}, nil, err
	}
	for _, env := range list {
		if env.Kind == environments.KindLocal {
			c, err := s.envs.Client(env)
			return env, c, err
		}
	}
	return environments.Environment{}, nil, selfupdate.ErrNoSelf
}

// findSelf finds nudge's own container on env: the server's on the local environment,
// the agent's on an agent environment.
func (s *server) findSelf(ctx context.Context, env environments.Environment, c docker.Client) (selfupdate.Target, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	if env.Kind != environments.KindAgent {
		return selfupdate.Find(ctx, c, s.self, s.dockerHost)
	}
	if s.agentSelf == nil {
		return selfupdate.Target{}, selfupdate.ErrNoSelf
	}
	self, host, err := s.agentSelf(ctx, env)
	if err != nil {
		return selfupdate.Target{}, err
	}
	return selfupdate.Find(ctx, c, self, host)
}

// selfContainer returns the ID and compose project of nudge's own container on env, or
// empty strings when there is none or it can't be told.
func (s *server) selfContainer(ctx context.Context, env environments.Environment, c docker.Client) (id, project string) {
	t, err := s.findSelf(ctx, env, c)
	if t.ID == "" || (err != nil && !errors.Is(err, selfupdate.ErrNoSocket)) {
		return "", ""
	}
	return t.ID, t.Project
}

// selfStack reports whether stack name on env is the compose project nudge's own
// container belongs to.
func (s *server) selfStack(ctx context.Context, env environments.Environment, name string) bool {
	if (env.Kind == environments.KindAgent && s.agentSelf == nil) || (env.Kind != environments.KindAgent && s.self.ID == "") {
		return false
	}
	c, err := s.envs.Client(env)
	if err != nil {
		return false
	}
	_, project := s.selfContainer(ctx, env, c)
	return project != "" && project == name
}

// errSelfContainer and errSelfStack refuse the generic recreate, update and stack runs on
// nudge's own container: stopping it would end the process doing the work halfway.
var (
	errSelfContainer = &statusError{code: http.StatusConflict, msg: "this is nudge's own container: update it from Settings (the gear) instead"}
	errSelfStack     = &statusError{code: http.StatusConflict, msg: "this stack runs nudge's own container: update nudge from Settings (the gear), and change this stack outside nudge"}
)

func (s *server) getSystemUpdate(w http.ResponseWriter, r *http.Request) {
	s.serveSystemUpdate(w, r, false)
}

func (s *server) checkSystemUpdate(w http.ResponseWriter, r *http.Request) {
	s.serveSystemUpdate(w, r, true)
}

func (s *server) serveSystemUpdate(w http.ResponseWriter, r *http.Request, check bool) {
	env, c, err := s.localEnvironment(r.Context())
	if err != nil && !errors.Is(err, selfupdate.ErrNoSelf) {
		s.internalError(w, "local environment", err)
		return
	}
	if err != nil {
		s.writeJSON(w, http.StatusOK, selfUpdateJSON{Version: s.version, Status: updates.Unknown, Reason: err.Error()})
		return
	}
	s.serveSelfStatus(w, r, env, c, s.version, check)
}

func (s *server) startSystemUpdate(w http.ResponseWriter, r *http.Request) {
	env, c, err := s.localEnvironment(r.Context())
	if errors.Is(err, selfupdate.ErrNoSelf) {
		s.writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, "local environment", err)
		return
	}
	s.startSelfUpdate(w, r, env, c)
}

// agentEnvironment is environment for the agent update endpoints.
func (s *server) agentEnvironment(w http.ResponseWriter, r *http.Request) (environments.Environment, docker.Client, bool) {
	env, c, ok := s.environment(w, r)
	if ok && env.Kind != environments.KindAgent {
		s.writeError(w, errNotAgent.code, errNotAgent.msg)
		return env, nil, false
	}
	return env, c, ok
}

func (s *server) getAgentUpdate(w http.ResponseWriter, r *http.Request) {
	if env, c, ok := s.agentEnvironment(w, r); ok {
		s.serveSelfStatus(w, r, env, c, env.AgentVersion, false)
	}
}

func (s *server) checkAgentUpdate(w http.ResponseWriter, r *http.Request) {
	if env, c, ok := s.agentEnvironment(w, r); ok {
		s.serveSelfStatus(w, r, env, c, env.AgentVersion, true)
	}
}

func (s *server) startAgentUpdate(w http.ResponseWriter, r *http.Request) {
	if env, c, ok := s.agentEnvironment(w, r); ok {
		s.startSelfUpdate(w, r, env, c)
	}
}

// serveSelfStatus answers the update state of nudge's container on env, from the last
// update check or, with check, a new one of the environment.
func (s *server) serveSelfStatus(w http.ResponseWriter, r *http.Request, env environments.Environment, c docker.Client, version string, check bool) {
	out := selfUpdateJSON{Version: version, Status: updates.Unknown}
	t, err := s.findSelf(r.Context(), env, c)
	if err != nil {
		reason, ok := selfReason(err)
		if !ok {
			s.dockerError(w, r, env, "find nudge's container", err)
			return
		}
		out.Image, out.Reason = t.Image, reason
		if t.ID == "" {
			s.writeJSON(w, http.StatusOK, out)
			return
		}
	} else {
		out.Image, out.CanUpdate = t.Image, true
		if selfupdate.Pinned(t.Image) {
			out.CanUpdate, out.Reason = false, selfupdate.ErrPinned.Error()
		}
	}
	if s.updates == nil {
		out.Error = "update checks aren't available"
		s.writeJSON(w, http.StatusOK, out)
		return
	}
	results, ok := s.selfResults(w, r, env, check)
	if !ok {
		return
	}
	out.Error = "not checked yet"
	for _, res := range results {
		if res.Image != t.Image {
			continue
		}
		if res.ImageID != "" && res.ImageID != t.ImageID {
			// Checked before the container last changed (an update since).
			out.Error = "checked before the last update; check again"
			break
		}
		at := res.CheckedAt
		out.Status, out.LocalDigest, out.RemoteDigest, out.Error, out.CheckedAt = res.Status, res.LocalDigest, res.RemoteDigest, res.Error, &at
		break
	}
	s.writeJSON(w, http.StatusOK, out)
}

// selfResults returns env's update check results, checked now with check; on false it
// has answered.
func (s *server) selfResults(w http.ResponseWriter, r *http.Request, env environments.Environment, check bool) ([]updates.Result, bool) {
	if !check {
		results, err := s.updates.List(r.Context(), env.ID)
		if err != nil {
			s.internalError(w, "list updates", err)
			return nil, false
		}
		return results, true
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), updateCheckTimeout)
	defer cancel()
	defer context.AfterFunc(s.done, cancel)()
	results, err := s.updates.Check(ctx, env)
	switch {
	case errors.Is(err, updates.ErrRunning):
		s.writeError(w, http.StatusConflict, "an update check is already running; try again when it's done")
		return nil, false
	case errors.Is(err, updates.ErrStore):
		s.internalError(w, "check updates", err)
		return nil, false
	case err != nil:
		s.dockerError(w, r, env, "check updates", err)
		return nil, false
	}
	return results, true
}

// startSelfUpdate pulls the image nudge's container on env runs by tag and, if that
// brought a newer one, starts the helper that replaces the container (202). The helper
// works on after this process stops; the old container comes back if the new one fails.
func (s *server) startSelfUpdate(w http.ResponseWriter, r *http.Request, env environments.Environment, c docker.Client) {
	// One update per environment at a time; a launched helper refuses the next one.
	done, ok := s.beginRecreate(env.ID, "self-update")
	if !ok {
		s.writeError(w, http.StatusConflict, selfupdate.ErrRunning.Error())
		return
	}
	defer done()
	t, err := s.findSelf(r.Context(), env, c)
	if err == nil && selfupdate.Pinned(t.Image) {
		err = selfupdate.ErrPinned
	}
	if err != nil {
		if reason, ok := selfReason(err); ok {
			s.writeError(w, http.StatusConflict, reason)
			return
		}
		s.dockerError(w, r, env, "find nudge's container", err)
		return
	}
	if err := s.pullWithAuth(r.Context(), c, t.Image); err != nil {
		s.replaceError(w, r, env, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dockerTimeout)
	defer cancel()
	img, err := c.ImageInspect(ctx, t.Image)
	if err != nil {
		s.dockerError(w, r, env, "inspect image", err)
		return
	}
	if img.ID == t.ImageID {
		s.writeJSON(w, http.StatusOK, selfUpdateStarted{Started: false})
		return
	}
	helper, err := selfupdate.Launch(ctx, c, t)
	if errors.Is(err, selfupdate.ErrRunning) {
		s.writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.dockerError(w, r, env, "start the update", err)
		return
	}
	s.log.Info("self-update started", "env", env.ID, "container", t.ID, "image", t.Image, "new_image", img.ID, "helper", helper, "ip", clientIP(r))
	s.writeJSON(w, http.StatusAccepted, selfUpdateStarted{Started: true})
}
