package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel"
)

// stackTimeout bounds one compose run; an up that pulls waits for every image.
const stackTimeout = pullTimeout

type stackSummaryResponse struct {
	Name string `json:"name"`
	// Containers counts the project's containers in any state, Running those running.
	Containers int `json:"containers"`
	Running    int `json:"running"`
}

type stackResponse struct {
	stackSummaryResponse
	Compose string `json:"compose"`
	Env     string `json:"env"`
	// Services are the project's containers by compose service, sorted by name.
	Services []stackServiceResponse `json:"services"`
}

type stackServiceResponse struct {
	Name       string                     `json:"name"`
	Containers []containerSummaryResponse `json:"containers"`
}

type stackFilesRequest struct {
	Name    string `json:"name"`
	Compose string `json:"compose"`
	Env     string `json:"env"`
}

// stackEvent is one line of a compose run's output stream. The last line has exit_code
// or error.
type stackEvent struct {
	Stream   string `json:"stream,omitempty"`
	Line     string `json:"line,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// stackDir holds environment envID's stacks, the source of truth for every kind of
// environment.
func (s *server) stackDir(envID int64) stacks.Dir {
	return stacks.NewDir(filepath.Join(s.stacksDir, strconv.FormatInt(envID, 10)))
}

// envID is the request's {env} as a number, 0 if it isn't one (s.environment answers
// that request 404).
func envID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("env"), 10, 64)
	return id
}

// stackKey names a stack in stackBusy.
func stackKey(envID int64, name string) string {
	return strconv.FormatInt(envID, 10) + "/" + name
}

// lockStack claims stack key ("<env>/<name>") for one change at a time; false if a
// change of it is running.
func (s *server) lockStack(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	env, _, _ := strings.Cut(key, "/")
	if s.stackBusy[key] || s.stackBusy[env+"/*"] {
		return false
	}
	s.stackBusy[key] = true
	return true
}

func (s *server) unlockStack(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.stackBusy, key)
}

// lockEnvironmentStacks claims every stack of environment envID: false if a change of
// one is running. Until unlockEnvironmentStacks, lockStack refuses them all.
func (s *server) lockEnvironmentStacks(envID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := strconv.FormatInt(envID, 10) + "/"
	for key := range s.stackBusy {
		if strings.HasPrefix(key, prefix) {
			return false
		}
	}
	s.stackBusy[prefix+"*"] = true
	return true
}

func (s *server) unlockEnvironmentStacks(envID int64) {
	s.unlockStack(strconv.FormatInt(envID, 10) + "/*")
}

// stackName returns the {id} path value if it can name a stack, else answers 404.
func (s *server) stackName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("id")
	if !stacks.ValidName(name) {
		s.writeError(w, http.StatusNotFound, "stack not found")
		return "", false
	}
	return name, true
}

// projectContainers lists a project's containers in any state, or every project's
// if project is empty.
func projectContainers(ctx context.Context, c docker.Client, project string) ([]container.Summary, error) {
	filter := stacks.ProjectLabel
	if project != "" {
		filter += "=" + project
	}
	res, err := c.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", filter)})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// stackContainers counts each project's containers, all and running.
func stackContainers(ctx context.Context, c docker.Client, project string) (map[string]stackSummaryResponse, error) {
	items, err := projectContainers(ctx, c, project)
	if err != nil {
		return nil, err
	}
	return countContainers(items), nil
}

// countContainers counts containers by project, all and running.
func countContainers(items []container.Summary) map[string]stackSummaryResponse {
	counts := map[string]stackSummaryResponse{}
	for _, ctr := range items {
		p := ctr.Labels[stacks.ProjectLabel]
		n := counts[p]
		n.Containers++
		if ctr.State == "running" {
			n.Running++
		}
		counts[p] = n
	}
	return counts
}

// stackServices groups a project's containers by service.
func stackServices(items []container.Summary) []stackServiceResponse {
	by := map[string][]containerSummaryResponse{}
	for _, ctr := range items {
		svc := ctr.Labels[stacks.ServiceLabel]
		by[svc] = append(by[svc], toContainerSummary(ctr))
	}
	resp := make([]stackServiceResponse, 0, len(by))
	for _, name := range slices.Sorted(maps.Keys(by)) {
		ctrs := by[name]
		slices.SortFunc(ctrs, func(a, b containerSummaryResponse) int { return strings.Compare(a.Name, b.Name) })
		resp = append(resp, stackServiceResponse{Name: name, Containers: ctrs})
	}
	return resp
}

// listStacks lists env's stacks with their containers.
func (s *server) listStacks(w http.ResponseWriter, r *http.Request) {
	s.dockerCall(w, r, "list stacks", func(ctx context.Context, c docker.Client) (any, error) {
		names, err := s.stackDir(envID(r)).List()
		if err != nil {
			return nil, err
		}
		counts, err := stackContainers(ctx, c, "")
		if err != nil {
			return nil, err
		}
		resp := make([]stackSummaryResponse, 0, len(names))
		for _, name := range names {
			n := counts[name]
			n.Name = name
			resp = append(resp, n)
		}
		return resp, nil
	})
}

// getStack answers a stack's files and containers.
func (s *server) getStack(w http.ResponseWriter, r *http.Request) {
	name, ok := s.stackName(w, r)
	if !ok {
		return
	}
	s.dockerCall(w, r, "stack", func(ctx context.Context, c docker.Client) (any, error) {
		f, err := s.stackDir(envID(r)).Read(name)
		if errors.Is(err, stacks.ErrNotFound) {
			return nil, &statusError{code: http.StatusNotFound, msg: "stack not found"}
		}
		if err != nil {
			return nil, err
		}
		items, err := projectContainers(ctx, c, name)
		if err != nil {
			return nil, err
		}
		n := countContainers(items)[name]
		n.Name = name
		return stackResponse{stackSummaryResponse: n, Compose: f.Compose, Env: f.Env, Services: stackServices(items)}, nil
	})
}

// checkStackTimeout bounds one compose config run.
const checkStackTimeout = 30 * time.Second

// maxStackChecks bounds the compose processes checks run at once.
const maxStackChecks = 4

// checkStack answers compose's complaints about stack files {"compose", "env"}:
// {"errors": [...]}, empty if compose accepts them. It writes nothing.
func (s *server) checkStack(w http.ResponseWriter, r *http.Request) {
	var req stackFilesRequest
	if !s.decodeJSON(w, r, &req) || !s.validStackFiles(w, req) {
		return
	}
	if s.stackChecker == nil {
		s.writeError(w, http.StatusServiceUnavailable, "stack checks are not available")
		return
	}
	select {
	case s.checks <- struct{}{}:
		defer func() { <-s.checks }()
	default:
		s.writeError(w, http.StatusTooManyRequests, "too many stack checks at once, try again")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), checkStackTimeout)
	defer cancel()
	errs, err := s.stackChecker(ctx, stacks.Files{Compose: req.Compose, Env: req.Env})
	if errors.Is(err, stacks.ErrNoCompose) {
		s.log.Error("check stack", "err", err)
		s.writeError(w, http.StatusServiceUnavailable, "docker-compose is not installed")
		return
	}
	if err != nil {
		s.internalError(w, "check stack", err)
		return
	}
	s.writeJSON(w, http.StatusOK, struct {
		Errors []string `json:"errors"`
	}{errs})
}

// validStackFiles answers 400 itself for files compose can't use.
func (s *server) validStackFiles(w http.ResponseWriter, req stackFilesRequest) bool {
	if strings.TrimSpace(req.Compose) == "" {
		s.writeError(w, http.StatusBadRequest, "compose must not be empty")
		return false
	}
	return true
}

// createStack writes a new stack {"name", "compose", "env"}; it isn't deployed.
func (s *server) createStack(w http.ResponseWriter, r *http.Request) {
	var req stackFilesRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if !stacks.ValidName(req.Name) {
		s.writeError(w, http.StatusBadRequest, "name must be lowercase letters, digits, - and _, starting with a letter or digit, at most 63 characters")
		return
	}
	auditTarget(r, req.Name)
	if !s.validStackFiles(w, req) {
		return
	}
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	key := stackKey(env.ID, req.Name)
	if !s.lockStack(key) {
		s.writeError(w, http.StatusConflict, "the stack is being changed")
		return
	}
	defer s.unlockStack(key)
	f := stacks.Files{Compose: req.Compose, Env: req.Env}
	err := s.stackDir(env.ID).Create(req.Name, f)
	if errors.Is(err, stacks.ErrExists) {
		s.writeError(w, http.StatusConflict, "a stack of that name exists")
		return
	}
	if err != nil {
		s.internalError(w, "create stack", err)
		return
	}
	s.writeJSON(w, http.StatusCreated, stackResponse{
		stackSummaryResponse: stackSummaryResponse{Name: req.Name}, Compose: f.Compose, Env: f.Env,
		Services: []stackServiceResponse{},
	})
}

// updateStack replaces a stack's files {"compose", "env"}; the running stack changes
// at its next deploy.
func (s *server) updateStack(w http.ResponseWriter, r *http.Request) {
	name, ok := s.stackName(w, r)
	if !ok {
		return
	}
	var req stackFilesRequest
	if !s.decodeJSON(w, r, &req) || !s.validStackFiles(w, req) {
		return
	}
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	key := stackKey(env.ID, name)
	if !s.lockStack(key) {
		s.writeError(w, http.StatusConflict, "the stack is being changed")
		return
	}
	defer s.unlockStack(key)
	err := s.stackDir(env.ID).Update(name, stacks.Files{Compose: req.Compose, Env: req.Env})
	if errors.Is(err, stacks.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "stack not found")
		return
	}
	if err != nil {
		s.internalError(w, "update stack", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removeStack deletes a stack's files; the stack must be down (no containers).
func (s *server) removeStack(w http.ResponseWriter, r *http.Request) {
	name, ok := s.stackName(w, r)
	if !ok {
		return
	}
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	key := stackKey(env.ID, name)
	if !s.lockStack(key) {
		s.writeError(w, http.StatusConflict, "the stack is being changed")
		return
	}
	defer s.unlockStack(key)
	s.dockerAction(w, r, "remove stack", "", dockerTimeout, func(ctx context.Context, c docker.Client) error {
		dir := s.stackDir(env.ID)
		if _, err := dir.Read(name); errors.Is(err, stacks.ErrNotFound) {
			return &statusError{code: http.StatusNotFound, msg: "stack not found"}
		}
		counts, err := stackContainers(ctx, c, name)
		if err != nil {
			return err
		}
		if counts[name].Containers > 0 {
			return &statusError{code: http.StatusConflict, msg: "the stack has containers: take it down first"}
		}
		if runner := s.stackRunner(env); runner != nil {
			// An agent on tunnel protocol 1 never got the files.
			if err := runner.Remove(ctx, name); err != nil && !errors.Is(err, stacks.ErrNotFound) && !errors.Is(err, tunnel.ErrNoControl) {
				return err
			}
		}
		if err := dir.Remove(name); err != nil && !errors.Is(err, stacks.ErrNotFound) {
			return err
		}
		return nil
	})
}

// upStack deploys a stack {"pull": bool}; pull also pulls newer images (an update).
func (s *server) upStack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pull bool `json:"pull"`
	}
	if !s.decodeOptionalJSON(w, r, &req) {
		return
	}
	s.runStack(w, r, "up", func(ctx context.Context, run stackRun) (int, error) {
		return run.runner.Up(ctx, run.name, stacks.UpOptions{Pull: req.Pull}, run.out)
	})
}

// downStack stops and removes a stack's containers {"volumes": bool}; volumes also
// removes its volumes.
func (s *server) downStack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Volumes bool `json:"volumes"`
	}
	if !s.decodeOptionalJSON(w, r, &req) {
		return
	}
	s.runStack(w, r, "down", func(ctx context.Context, run stackRun) (int, error) {
		return run.runner.Down(ctx, run.name, req.Volumes, run.out)
	})
}

// decodeOptionalJSON is decodeJSON for a body that may be left out.
func (s *server) decodeOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(body).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body: "+jsonErrorKind(err))
		return false
	}
	// Read to the end, so net/http watches the connection and cancels the request when
	// the client goes away.
	_, _ = io.Copy(io.Discard, body)
	return true
}

// runnerError answers a runner's failure before compose's output started.
func (s *server) runnerError(w http.ResponseWriter, r *http.Request, env environments.Environment, msg string, err error) {
	var bind *stacks.BindError
	switch {
	case errors.As(err, &bind):
		// Names paths and mounts of nudge's own container so the fix is clear. Every
		// authenticated user sees it; there are no roles yet, so that's everyone who
		// can deploy (revisit when roles exist).
		s.writeError(w, http.StatusConflict, bind.Error())
	case errors.Is(err, stacks.ErrNoCompose):
		s.log.Error(msg, "env", env.ID, "err", err)
		s.writeError(w, http.StatusServiceUnavailable, "docker-compose is not installed")
	case errors.Is(err, tunnel.ErrNoControl):
		s.writeError(w, http.StatusConflict, "update the agent to use stacks")
	case errors.Is(err, stacks.ErrBusy):
		s.writeError(w, http.StatusConflict, "the stack is being changed")
	case docker.Unreachable(err) || errors.Is(err, context.DeadlineExceeded):
		s.dockerError(w, r, env, msg, err)
	case errors.Is(err, stacks.ErrNotFound):
		s.log.Warn(msg, "env", env.ID, "err", err)
		s.writeError(w, http.StatusBadGateway, "the agent lost the stack's files; try again")
	default:
		s.internalError(w, msg, err)
	}
}

var (
	errNoStackRunner = &statusError{code: http.StatusConflict, msg: "stacks can't be deployed to this environment"}
	errStackBusy     = &statusError{code: http.StatusConflict, msg: "the stack is being changed"}
	errNoStack       = &statusError{code: http.StatusNotFound, msg: "stack not found"}
)

// startStack claims stack name of env for one change at a time and syncs its files to
// the environment's runner. The caller defers unlock.
func (s *server) startStack(ctx context.Context, env environments.Environment, name string) (stacks.Runner, func(), error) {
	return s.startStackEdit(ctx, env, name, nil)
}

// startStackEdit is startStack with edit, if not nil, changing the stack's files once it
// is claimed and before they sync; the change is written to the stacks directory. If the
// write or the sync fails, the files are put back before the stack is released.
func (s *server) startStackEdit(ctx context.Context, env environments.Environment, name string, edit func(stacks.Files) stacks.Files) (stacks.Runner, func(), error) {
	runner := s.stackRunner(env)
	if runner == nil {
		return nil, nil, errNoStackRunner
	}
	key := stackKey(env.ID, name)
	if !s.lockStack(key) {
		return nil, nil, errStackBusy
	}
	unlock := func() { s.unlockStack(key) }
	f, err := s.stackDir(env.ID).Read(name)
	if errors.Is(err, stacks.ErrNotFound) {
		unlock()
		return nil, nil, errNoStack
	}
	if err != nil {
		unlock()
		return nil, nil, &internalFailure{msg: "read stack", err: err}
	}
	edited := edit != nil
	original := f
	if edited {
		f = edit(f)
	}
	// fail puts back the files an edit changed, then releases the stack.
	fail := func(err error) (stacks.Runner, func(), error) {
		if edited {
			if err := s.stackDir(env.ID).Update(name, original); err != nil {
				s.log.Error("restore stack after a failed edit", "env", env.ID, "stack", name, "err", err)
			}
		}
		unlock()
		return nil, nil, err
	}
	if edited {
		if err := s.stackDir(env.ID).Update(name, f); err != nil {
			return fail(&internalFailure{msg: "write stack", err: err})
		}
	}
	if err := runner.Sync(ctx, name, f); err != nil {
		return fail(fmt.Errorf("sync stack: %w", err))
	}
	return runner, unlock, nil
}

// stackStartError answers a failed startStack.
func (s *server) stackStartError(w http.ResponseWriter, r *http.Request, env environments.Environment, err error) {
	var se *statusError
	var inf *internalFailure
	switch {
	case errors.As(err, &se):
		s.writeError(w, se.code, se.msg)
	case errors.As(err, &inf):
		s.internalError(w, inf.msg, inf.err)
	default:
		s.runnerError(w, r, env, "sync stack", err)
	}
}

// stackRun is what a compose run of runStack works with.
type stackRun struct {
	env    environments.Environment
	c      docker.Client
	runner stacks.Runner
	name   string
	// out sends a line of compose's output.
	out func(stacks.Line)
	// wait, if set, has compose wait for running and healthy services (deployments).
	wait time.Duration
}

// runStack syncs a stack to its environment's runner and runs compose, streaming the
// output as JSON lines (application/x-ndjson). Errors before compose starts are ordinary
// JSON errors; the stream ends with the exit code or an error line. The run stops if the
// client goes away, like Ctrl-C on docker compose.
func (s *server) runStack(w http.ResponseWriter, r *http.Request, op string, run func(ctx context.Context, run stackRun) (int, error)) {
	name, ok := s.stackName(w, r)
	if !ok {
		return
	}
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), stackTimeout)
	defer cancel()
	runner, unlock, err := s.startStack(ctx, env, name)
	if err != nil {
		s.stackStartError(w, r, env, err)
		return
	}
	defer unlock()
	s.log.Info("stack "+op, "env", env.ID, "stack", name, "ip", clientIP(r))

	// The header goes out with the first line, so a compose that can't start still gets
	// an ordinary error.
	started := false
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	alive := true
	send := func(ev stackEvent) {
		if !alive {
			return
		}
		if !started {
			started = true
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
		}
		alive = enc.Encode(ev) == nil && rc.Flush() == nil
		if !alive {
			// Nobody reads the output any more: stop compose like a disconnect.
			cancel()
		}
	}
	out := func(l stacks.Line) { send(stackEvent{Stream: l.Stream, Line: l.Text}) }
	code, err := run(ctx, stackRun{env: env, c: c, runner: runner, name: name, out: out})
	switch {
	case err == nil:
		if code != 0 {
			auditStatus(r, http.StatusBadGateway)
		}
		send(stackEvent{ExitCode: &code})
	case r.Context().Err() != nil || !alive:
		// The client went away and the run was killed: 499 as in nginx, "client closed
		// request", so the entry doesn't read as a success.
		auditStatus(r, 499)
	case !started:
		s.runnerError(w, r, env, "stack "+op, err)
	default:
		s.log.Warn("stack "+op, "env", env.ID, "stack", name, "err", err)
		auditStatus(r, http.StatusBadGateway)
		msg := "compose did not finish"
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "compose did not finish in time"
		}
		send(stackEvent{Error: msg})
	}
}
