package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/deployhooks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel"
	"git.zefirlabs.net/zefir/nudge/backend/internal/updates"
)

const (
	// maxHookBody bounds a trigger's body, read whole before its signature is checked.
	maxHookBody = 1 << 20
	// hookReadTimeout bounds reading a trigger's body.
	hookReadTimeout = 30 * time.Second
	// hookWaitTimeout bounds how long ?wait=1 waits for a run.
	hookWaitTimeout = 5 * time.Minute
)

type deployHookJSON struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Kind            string     `json:"kind"`
	Target          string     `json:"target"`
	TagFilter       string     `json:"tag_filter"`
	CreatedBy       string     `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	LastTriggeredAt *time.Time `json:"last_triggered_at"`
	// LastStatus is the newest run's status, "" before the first.
	LastStatus string `json:"last_status"`
	// URL is the trigger URL; not a secret on its own.
	URL string `json:"url"`
}

// deployHookSecretJSON is a webhook with its secret, answered only on create and rotate.
type deployHookSecretJSON struct {
	deployHookJSON
	Secret string `json:"secret"`
}

type deployHookRunJSON struct {
	ID          int64      `json:"id"`
	Trigger     string     `json:"trigger"`
	Status      string     `json:"status"`
	QueuedAt    time.Time  `json:"queued_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	ImageBefore string     `json:"image_before"`
	ImageAfter  string     `json:"image_after"`
	Error       string     `json:"error"`
}

func (s *server) toDeployHookJSON(r *http.Request, h deployhooks.Hook) deployHookJSON {
	return deployHookJSON{
		ID: h.ID, Name: h.Name, Kind: string(h.Kind), Target: h.Target, TagFilter: h.TagFilter,
		CreatedBy: h.Owner, CreatedAt: h.CreatedAt, LastTriggeredAt: h.LastTriggeredAt,
		LastStatus: string(h.LastStatus),
		URL:        s.serverURL(r) + "/api/v1/hooks/" + h.ID,
	}
}

func toDeployHookRunJSON(run deployhooks.Run) deployHookRunJSON {
	return deployHookRunJSON{
		ID: run.ID, Trigger: string(run.Trigger), Status: string(run.Status), QueuedAt: run.QueuedAt,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, ImageBefore: run.ImageBefore, ImageAfter: run.ImageAfter, Error: run.Error,
	}
}

// hookActor is r's user as a webhook manager.
func (s *server) hookActor(r *http.Request) deployhooks.Actor {
	u, _ := userFrom(r.Context())
	return deployhooks.Actor{UserID: u.ID, Admin: isAdmin(u)}
}

// deployHooksReady answers 503 if deploy webhooks aren't available.
func (s *server) deployHooksReady(w http.ResponseWriter) bool {
	if s.deployHooks == nil {
		s.writeError(w, http.StatusServiceUnavailable, "deploy webhooks aren't available")
		return false
	}
	return true
}

func (s *server) deployHookError(w http.ResponseWriter, msg string, err error) {
	var inputErr *deployhooks.InputError
	switch {
	case errors.As(err, &inputErr):
		s.writeError(w, http.StatusBadRequest, inputErr.Msg)
	case errors.Is(err, deployhooks.ErrNotFound):
		s.writeError(w, http.StatusNotFound, "webhook not found")
	case errors.Is(err, deployhooks.ErrTaken), errors.Is(err, deployhooks.ErrTooMany):
		s.writeError(w, http.StatusConflict, err.Error())
	default:
		s.internalError(w, msg, err)
	}
}

// listDeployHooks answers the environment's webhooks the user may see; ?container= or
// ?stack= narrows them to one target.
func (s *server) listDeployHooks(w http.ResponseWriter, r *http.Request) {
	if !s.deployHooksReady(w) {
		return
	}
	q := r.URL.Query()
	var kind deployhooks.Kind
	var target string
	switch ct, st := q.Get("container"), q.Get("stack"); {
	case ct != "" && st != "":
		s.writeError(w, http.StatusBadRequest, "filter by container or by stack, not both")
		return
	case ct != "":
		kind, target = deployhooks.KindContainer, ct
	case st != "":
		kind, target = deployhooks.KindStack, st
	}
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	list, err := s.deployHooks.List(r.Context(), s.hookActor(r), env.ID, kind, target)
	if err != nil {
		s.internalError(w, "list webhooks", err)
		return
	}
	out := make([]deployHookJSON, 0, len(list))
	for _, h := range list {
		out = append(out, s.toDeployHookJSON(r, h))
	}
	s.writeJSON(w, http.StatusOK, out)
}

type createDeployHookRequest struct {
	Name      string `json:"name"`
	Container string `json:"container"`
	Stack     string `json:"stack"`
	TagFilter string `json:"tag_filter"`
}

// createDeployHook creates a webhook for a container (an ID or a name; stored by name,
// so it outlives a recreate) or a stack, and answers its secret, shown only now.
func (s *server) createDeployHook(w http.ResponseWriter, r *http.Request) {
	if !s.deployHooksReady(w) {
		return
	}
	var req createDeployHookRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	auditTarget(r, req.Name)
	if (req.Container == "") == (req.Stack == "") {
		s.writeError(w, http.StatusBadRequest, "name a container or a stack")
		return
	}
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	in := deployhooks.Input{Name: req.Name, Environment: env.ID, TagFilter: req.TagFilter}
	if req.Container != "" {
		in.Kind = deployhooks.KindContainer
		if !objectID.MatchString(req.Container) {
			s.writeError(w, http.StatusNotFound, "container not found")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), dockerTimeout)
		res, err := c.ContainerInspect(ctx, req.Container, client.ContainerInspectOptions{})
		cancel()
		if docker.NotFound(err) {
			s.envs.Seen(r.Context(), &env)
			s.writeError(w, http.StatusNotFound, "container not found")
			return
		}
		if err != nil {
			s.dockerError(w, r, env, "webhook container", err)
			return
		}
		in.Target = strings.TrimPrefix(res.Container.Name, "/")
	} else {
		in.Kind = deployhooks.KindStack
		in.Target = req.Stack
		if !stacks.ValidName(req.Stack) || !s.stackExists(env.ID, req.Stack) {
			s.writeError(w, http.StatusNotFound, "stack not found")
			return
		}
	}
	h, secret, err := s.deployHooks.Create(r.Context(), s.hookActor(r), in)
	if err != nil {
		s.deployHookError(w, "create webhook", err)
		return
	}
	s.log.Info("webhook created", "env", env.ID, "hook", h.ID, "kind", h.Kind, "target", h.Target, "ip", clientIP(r))
	s.writeJSON(w, http.StatusCreated, deployHookSecretJSON{deployHookJSON: s.toDeployHookJSON(r, h), Secret: secret})
}

// stackExists reports whether env has a stack called name.
func (s *server) stackExists(env int64, name string) bool {
	_, err := s.stackDir(env).Read(name)
	return err == nil
}

func (s *server) deleteDeployHook(w http.ResponseWriter, r *http.Request) {
	if !s.deployHooksReady(w) {
		return
	}
	if err := s.deployHooks.Delete(r.Context(), s.hookActor(r), envID(r), r.PathValue("id")); err != nil {
		s.deployHookError(w, "delete webhook", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rotateDeployHookSecret gives a webhook a new secret; the old one stops working at once.
func (s *server) rotateDeployHookSecret(w http.ResponseWriter, r *http.Request) {
	if !s.deployHooksReady(w) {
		return
	}
	h, secret, err := s.deployHooks.Rotate(r.Context(), s.hookActor(r), envID(r), r.PathValue("id"))
	if err != nil {
		s.deployHookError(w, "rotate webhook secret", err)
		return
	}
	s.log.Info("webhook secret rotated", "env", h.Environment, "hook", h.ID, "ip", clientIP(r))
	s.writeJSON(w, http.StatusOK, deployHookSecretJSON{deployHookJSON: s.toDeployHookJSON(r, h), Secret: secret})
}

// listDeployHookRuns answers a webhook's runs, newest first.
func (s *server) listDeployHookRuns(w http.ResponseWriter, r *http.Request) {
	if !s.deployHooksReady(w) {
		return
	}
	runs, err := s.deployHooks.Runs(r.Context(), s.hookActor(r), envID(r), r.PathValue("id"))
	if err != nil {
		s.deployHookError(w, "list webhook runs", err)
		return
	}
	out := make([]deployHookRunJSON, 0, len(runs))
	for _, run := range runs {
		out = append(out, toDeployHookRunJSON(run))
	}
	s.writeJSON(w, http.StatusOK, out)
}

type triggerResponse struct {
	RunID  int64  `json:"run_id,omitempty"`
	Status string `json:"status"`
}

// triggerDeployHook is public: the webhook's secret, as a bearer token or as the key of
// the body's HMAC signature, is the credential. Failed checks count against the client
// IP's failed-login limit. The deploy runs as the webhook's creator, in the background,
// unless ?wait=1 asks to wait for it.
func (s *server) triggerDeployHook(w http.ResponseWriter, r *http.Request) {
	if !s.deployHooksReady(w) {
		return
	}
	// The IP's limit first, so a blocked client gets nothing read; the attempt is handed
	// back only once the credential turned out valid.
	ip := clientIP(r)
	succeeded, err := s.auth.Attempt(ip)
	var limited *auth.RateLimitedError
	if errors.As(err, &limited) {
		s.log.Warn("webhook trigger rate limited", "ip", ip)
		retryAfter(w, limited.RetryAfter)
		s.writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	if r.ContentLength > maxHookBody {
		s.writeError(w, http.StatusRequestEntityTooLarge, "the body is too large")
		return
	}
	// A slow sender can't hold the request open; not every writer supports deadlines.
	// The deadline goes again once the body is in: net/http's read that notices a client
	// going away would hit it and cancel a ?wait=1 caller's request.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(hookReadTimeout))
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
	_ = rc.SetReadDeadline(time.Time{})
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		s.writeError(w, http.StatusRequestEntityTooLarge, "the body is too large")
		return
	case err != nil:
		s.writeError(w, http.StatusBadRequest, "the body could not be read")
		return
	}
	h, trigger, err := s.deployHooks.Authenticate(r.Context(), r.PathValue("id"), r.Header, body)
	if errors.Is(err, deployhooks.ErrUnauthorized) {
		s.log.Warn("webhook trigger refused", "ip", ip)
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if err != nil {
		s.internalError(w, "authenticate webhook", err)
		return
	}
	succeeded()
	auditUser(r, auth.User{ID: h.CreatedBy, Username: h.Owner, Token: "hook:" + h.Name})
	auditEnvironment(r, strconv.FormatInt(h.Environment, 10))
	wait, ok := s.queryBool(w, r, "wait")
	if !ok {
		return
	}

	if deployhooks.Skipped(h, trigger, r.Header, body) {
		run, err := s.deployHooks.Skip(r.Context(), h, trigger)
		if err != nil {
			s.triggerError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, triggerResponse{RunID: run.ID, Status: string(deployhooks.StatusSkipped)})
		return
	}
	ticket, err := s.deployHooks.Trigger(r.Context(), h, trigger, s.deployHook)
	if err != nil {
		s.triggerError(w, err)
		return
	}
	s.log.Info("webhook triggered", "env", h.Environment, "hook", h.ID, "run", ticket.RunID, "trigger", trigger, "ip", ip)
	if !wait {
		s.writeJSON(w, http.StatusAccepted, triggerResponse{RunID: ticket.RunID, Status: string(deployhooks.StatusQueued)})
		return
	}
	run, finished, err := s.deployHooks.Wait(r.Context(), h, ticket, hookWaitTimeout)
	switch {
	case r.Context().Err() != nil:
		// The client went away; the run goes on.
		return
	case errors.Is(err, deployhooks.ErrNotFound):
		// Deleted meanwhile.
		s.writeError(w, http.StatusNotFound, "webhook not found")
		return
	case err != nil:
		s.internalError(w, "webhook run", err)
		return
	case !finished:
		s.writeJSON(w, http.StatusAccepted, toDeployHookRunJSON(run))
	case run.Status == deployhooks.StatusFailed:
		s.writeJSON(w, http.StatusBadGateway, toDeployHookRunJSON(run))
	default:
		s.writeJSON(w, http.StatusOK, toDeployHookRunJSON(run))
	}
}

func (s *server) triggerError(w http.ResponseWriter, err error) {
	var limited *deployhooks.RateLimitedError
	switch {
	case errors.As(err, &limited):
		retryAfter(w, limited.RetryAfter)
		s.writeError(w, http.StatusTooManyRequests, "the webhook was triggered too often, try again later")
	case errors.Is(err, deployhooks.ErrClosed):
		s.writeError(w, http.StatusServiceUnavailable, "nudge is shutting down")
	default:
		s.internalError(w, "trigger webhook", err)
	}
}

func retryAfter(w http.ResponseWriter, d time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(d.Seconds())))))
}

// deployHook updates a webhook's target as its creator: the container update or stack
// update of the API, on any kind of environment. The outcome's error is a safe message;
// the log has the details.
func (s *server) deployHook(ctx context.Context, h deployhooks.Hook) deployhooks.Outcome {
	failed := func(err error) deployhooks.Outcome {
		s.log.Warn("webhook deploy", "env", h.Environment, "hook", h.ID, "kind", h.Kind, "target", h.Target, "err", err)
		return deployhooks.Outcome{Status: deployhooks.StatusFailed, Error: deployError(err)}
	}
	env, err := s.envs.Get(ctx, h.Environment)
	if err != nil {
		return failed(err)
	}
	c, err := s.envs.Client(env)
	if err != nil {
		return failed(err)
	}
	if h.Kind == deployhooks.KindContainer {
		res, err := s.updateContainerImage(ctx, env, c, h.Target, h.Owner)
		if err != nil {
			return failed(err)
		}
		status := deployhooks.StatusUnchanged
		if res.Updated {
			status = deployhooks.StatusUpdated
		}
		return deployhooks.Outcome{Status: status, ImageBefore: res.PreviousImageID, ImageAfter: res.ImageID}
	}
	ctx, cancel := context.WithTimeout(ctx, stackTimeout)
	defer cancel()
	runner, unlock, err := s.startStack(ctx, env, h.Target)
	if err != nil {
		return failed(err)
	}
	defer unlock()
	code, changes, err := s.pullAndDeploy(ctx, stackRun{env: env, c: c, runner: runner, name: h.Target, out: func(stacks.Line) {}}, h.Owner)
	before, after := stackImages(changes)
	switch {
	case err != nil:
		out := failed(err)
		out.ImageBefore, out.ImageAfter = before, after
		return out
	case code != 0:
		return deployhooks.Outcome{Status: deployhooks.StatusFailed, ImageBefore: before, ImageAfter: after, Error: fmt.Sprintf("compose exited with code %d", code)}
	case len(changes) == 0:
		return deployhooks.Outcome{Status: deployhooks.StatusUnchanged}
	default:
		return deployhooks.Outcome{Status: deployhooks.StatusUpdated, ImageBefore: before, ImageAfter: after}
	}
}

// stackImages lists the images of a stack's changed services as "service=ID", before
// and after.
func stackImages(changes []updates.Change) (string, string) {
	var before, after []string
	for _, c := range changes {
		before = append(before, c.Service+"="+c.OldImageID)
		after = append(after, c.Service+"="+c.NewImageID)
	}
	return strings.Join(before, ","), strings.Join(after, ",")
}

// deployError says why a webhook's deploy failed without the daemon's words, which can
// carry secrets.
func deployError(err error) string {
	var pf *pullFailure
	var se *statusError
	switch {
	case errors.Is(err, context.Canceled):
		return "the run was cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "the environment's Docker host did not answer in time"
	case docker.Unreachable(err):
		return "the environment is offline"
	case errors.Is(err, environments.ErrNotFound):
		return "environment not found"
	case errors.Is(err, errRegistryDecrypt):
		return "the stored registry password can't be decrypted: enter it again under Registries"
	case errors.As(err, &pf):
		switch {
		case docker.NotFound(pf.err):
			return "pull failed: the image or tag was not found"
		case docker.Unauthorized(pf.err):
			return "pull failed: the registry refused access"
		default:
			return "pull failed"
		}
	case errors.Is(err, errPinnedImage), errors.Is(err, errRecreating), errors.Is(err, errNoStackRunner),
		errors.Is(err, errStackBusy), errors.Is(err, errNoStack):
		// nudge's own words.
		errors.As(err, &se)
		return se.msg
	case errors.Is(err, stacks.ErrNoCompose):
		return "docker-compose is not installed"
	case errors.Is(err, tunnel.ErrNoControl):
		return "update the agent to use stacks"
	case errors.Is(err, stacks.ErrBusy):
		return "the stack is being changed"
	case errors.As(err, &se):
		// A refused create or start quotes the daemon.
		return "the container could not be replaced; the old one was restored if possible (see the server log)"
	case docker.NotFound(err):
		return "container not found"
	default:
		return "the update failed (see the server log)"
	}
}
