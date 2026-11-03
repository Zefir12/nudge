package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/audit"
	"git.zefirlabs.net/zefir/nudge/backend/internal/deployments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/gitstacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

// gitPollUser is the name polling's updates and audit entries go under.
const gitPollUser = "git-poll"

type stackGitRequest struct {
	RepoURL     string `json:"repo_url"`
	Ref         string `json:"ref"`
	ComposePath string `json:"compose_path"`
	EnvPath     string `json:"env_path"`
	Auth        string `json:"auth"`
	Username    string `json:"username"`
	// Secret is the token or the SSH private key; left out, the stored one stays.
	Secret      *string `json:"secret"`
	KnownHosts  string  `json:"known_hosts"`
	PollSeconds int64   `json:"poll_seconds"`
}

func (req stackGitRequest) input() gitstacks.Input {
	return gitstacks.Input{
		RepoURL: req.RepoURL, Ref: req.Ref, ComposePath: req.ComposePath, EnvPath: req.EnvPath, Auth: gitstacks.AuthKind(req.Auth),
		Username: req.Username, Secret: req.Secret, KnownHosts: req.KnownHosts, Poll: time.Duration(req.PollSeconds) * time.Second,
	}
}

// stackGitResponse is a stack's git source; its secret never leaves the server.
type stackGitResponse struct {
	RepoURL     string    `json:"repo_url"`
	Ref         string    `json:"ref"`
	ComposePath string    `json:"compose_path"`
	EnvPath     string    `json:"env_path"`
	Auth        string    `json:"auth"`
	Username    string    `json:"username"`
	KnownHosts  string    `json:"known_hosts"`
	PollSeconds int64     `json:"poll_seconds"`
	Commit      string    `json:"commit"`
	LastError   string    `json:"last_error"`
	CheckedAt   time.Time `json:"checked_at,omitzero"`
	PulledAt    time.Time `json:"pulled_at,omitzero"`
}

func toStackGit(src gitstacks.Source) *stackGitResponse {
	return &stackGitResponse{
		RepoURL: src.RepoURL, Ref: src.Ref, ComposePath: src.ComposePath, EnvPath: src.EnvPath, Auth: string(src.Auth),
		Username: src.Username, KnownHosts: src.KnownHosts, PollSeconds: int64(src.Poll / time.Second), Commit: src.Commit,
		LastError: src.LastError, CheckedAt: src.CheckedAt, PulledAt: src.PulledAt,
	}
}

// stackSource returns a stack's git source, nil for a stack that has none.
func (s *server) stackSource(ctx context.Context, envID int64, name string) (*gitstacks.Source, error) {
	if s.gitStacks == nil {
		return nil, nil
	}
	src, err := s.gitStacks.Get(ctx, envID, name)
	if errors.Is(err, gitstacks.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &src, nil
}

// gitInputError answers a refused source, or a repository that couldn't be read, itself.
func (s *server) gitInputError(w http.ResponseWriter, msg string, err error) {
	var ie *gitstacks.InputError
	var ge *gitstacks.Error
	switch {
	case errors.As(err, &ie):
		s.writeError(w, http.StatusBadRequest, ie.Msg)
	case errors.As(err, &ge):
		s.log.Warn(msg, "err", err)
		s.writeError(w, http.StatusBadGateway, ge.Msg)
	default:
		s.internalError(w, msg, err)
	}
}

// setStackGit makes a stack's compose file come from a repository {see stackGitRequest},
// or changes the repository. The files are read now and written; the running stack changes
// at its next deploy.
func (s *server) setStackGit(w http.ResponseWriter, r *http.Request) {
	name, ok := s.stackName(w, r)
	if !ok {
		return
	}
	var req stackGitRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if s.gitStacks == nil {
		s.writeError(w, http.StatusServiceUnavailable, "git stacks are not available")
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
	dir := s.stackDir(env.ID)
	f, err := dir.Read(name)
	if errors.Is(err, stacks.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "stack not found")
		return
	}
	if err != nil {
		s.internalError(w, "read stack", err)
		return
	}
	src, err := s.gitStacks.Prepare(r.Context(), env.ID, name, req.input())
	if err != nil {
		s.gitInputError(w, "set stack source", err)
		return
	}
	res, err := s.gitStacks.Fetch(r.Context(), src)
	if err != nil {
		s.gitInputError(w, "set stack source", err)
		return
	}
	updated := filesFromRepo(f, src, res)
	if err := dir.Update(name, updated); err != nil {
		s.internalError(w, "write stack", err)
		return
	}
	if err := s.gitStacks.Save(r.Context(), src, res.Commit); err != nil {
		if err := dir.Update(name, f); err != nil {
			s.log.Error("restore stack after a failed save", "env", env.ID, "stack", name, "err", err)
		}
		s.internalError(w, "save stack source", err)
		return
	}
	saved, err := s.gitStacks.Get(r.Context(), env.ID, name)
	if err != nil {
		s.internalError(w, "get stack source", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toStackGit(saved))
}

// removeStackGit detaches a stack from its repository: it keeps the files it has, which
// can be edited again.
func (s *server) removeStackGit(w http.ResponseWriter, r *http.Request) {
	name, ok := s.stackName(w, r)
	if !ok {
		return
	}
	if s.gitStacks == nil {
		s.writeError(w, http.StatusNotFound, "the stack has no git source")
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
	err := s.gitStacks.Remove(r.Context(), env.ID, name)
	if errors.Is(err, gitstacks.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "the stack has no git source")
		return
	}
	if err != nil {
		s.internalError(w, "remove stack source", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pullStackGit gets a stack's files from its repository and deploys them, streaming
// compose's output like up. A failed deploy puts the files back.
func (s *server) pullStackGit(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var pull *gitPull
	start := func(ctx context.Context, env environments.Environment, name string) (stacks.Runner, func(), error) {
		src, err := s.stackSource(ctx, env.ID, name)
		if err != nil {
			return nil, nil, &internalFailure{msg: "get stack source", err: err}
		}
		if src == nil {
			return nil, nil, &statusError{code: http.StatusConflict, msg: "the stack has no git source"}
		}
		runner, p, unlock, err := s.startStackPull(ctx, env, name, nil)
		if err == nil && p == nil { // detached just now
			unlock()
			return nil, nil, &statusError{code: http.StatusConflict, msg: "the stack has no git source"}
		}
		pull = p
		return runner, unlock, err
	}
	s.runStackFrom(w, r, "pull", start, func(ctx context.Context, run stackRun) (int, error) {
		run.out(stacks.Line{Stream: "stdout", Text: "pulled commit " + shortCommit(pull.commit)})
		code, _, err := s.pullAndDeploy(ctx, run, u.Username)
		s.finishGitPull(ctx, run.env, run.name, pull, err == nil && code == 0)
		if err != nil || code != 0 {
			run.out(stacks.Line{Stream: "stderr", Text: "the deploy failed: the stack's files stay at the previous commit"})
		}
		return code, err
	})
}

// shortCommit is a commit as people read it.
func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// filesFromRepo is f with what a pull got: the compose file replaced, the env file's
// variables set in .env, whose other lines (secrets, a deploy's variables) stay.
func filesFromRepo(f stacks.Files, src gitstacks.Source, res gitstacks.Result) stacks.Files {
	f.Compose = res.Compose
	if src.EnvPath != "" {
		f.Env = deployments.MergeEnv(f.Env, res.Env)
	}
	return f
}

// gitPull is a pull made at the start of a deploy, until the deploy's outcome is known.
type gitPull struct {
	commit   string
	original stacks.Files
	// changed says the pull changed the stack's files.
	changed bool
}

// startStackPull is startStackEdit that first gets the files from the stack's repository,
// if it has one. The pull is nil for a stack that doesn't; otherwise the caller ends it
// with finishGitPull while it holds the stack.
func (s *server) startStackPull(ctx context.Context, env environments.Environment, name string, edit func(stacks.Files) stacks.Files) (stacks.Runner, *gitPull, func(), error) {
	var pull *gitPull
	runner, unlock, err := s.startStackEdit(ctx, env, name, func(f stacks.Files) (stacks.Files, error) {
		src, err := s.stackSource(ctx, env.ID, name)
		if err != nil {
			return f, &internalFailure{msg: "get stack source", err: err}
		}
		if src != nil {
			res, err := s.gitStacks.Fetch(ctx, *src)
			if err != nil {
				s.recordGitFailure(ctx, env.ID, name, "", err)
				return f, err
			}
			original := f
			f = filesFromRepo(f, *src, res)
			pull = &gitPull{commit: res.Commit, original: original, changed: f != original}
		}
		if edit != nil {
			f = edit(f)
		}
		return f, nil
	})
	if err != nil {
		if pull != nil {
			// The files went back. Not the commit's fault, so polling may try it again.
			s.recordGitFailure(ctx, env.ID, name, "", &gitstacks.Error{Msg: "could not start the deploy of commit " + shortCommit(pull.commit) + ": " + deployError(err)})
		}
		return nil, nil, nil, err
	}
	return runner, pull, unlock, nil
}

// finishGitPull ends a pull of startStackPull, nil being none. After a deploy that worked
// the stack is at the pulled commit. After one that failed its files go back, so its next
// start uses what worked and the commit shown stays true; polling waits for a newer commit.
func (s *server) finishGitPull(ctx context.Context, env environments.Environment, name string, pull *gitPull, ok bool) {
	if pull == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dockerTimeout)
	defer cancel()
	if ok {
		if err := s.gitStacks.RecordDeployed(ctx, env.ID, name, pull.commit); err != nil {
			s.log.Error("record stack commit", "env", env.ID, "stack", name, "err", err)
		}
		return
	}
	if err := s.stackDir(env.ID).Update(name, pull.original); err != nil {
		s.log.Error("restore stack after a failed deploy", "env", env.ID, "stack", name, "err", err)
	}
	msg := "the deploy of commit " + shortCommit(pull.commit) + " failed"
	if err := s.gitStacks.RecordFailure(ctx, env.ID, name, pull.commit, msg); err != nil {
		s.log.Error("record stack pull failure", "env", env.ID, "stack", name, "err", err)
	}
}

// recordGitFailure keeps why a pull failed for the stack's page.
func (s *server) recordGitFailure(ctx context.Context, envID int64, name, seen string, err error) {
	msg := "could not read the repository"
	var ge *gitstacks.Error
	if errors.As(err, &ge) {
		msg = ge.Msg
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dockerTimeout)
	defer cancel()
	if err := s.gitStacks.RecordFailure(ctx, envID, name, seen, msg); err != nil {
		s.log.Error("record stack pull failure", "env", envID, "stack", name, "err", err)
	}
}

// pollDeploy deploys a stack whose repository moved, found by polling. A stack being
// changed is left for the next poll: its checked time passed, not its commit.
func (s *server) pollDeploy(ctx context.Context, src gitstacks.Source) {
	log := s.log.With("env", src.Environment, "stack", src.Stack)
	env, err := s.envs.Get(ctx, src.Environment)
	if err != nil {
		log.Warn("git poll", "err", err)
		return
	}
	c, err := s.envs.Client(env)
	if err != nil {
		log.Warn("git poll", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, stackTimeout)
	defer cancel()
	runner, pull, unlock, err := s.startStackPull(ctx, env, src.Stack, nil)
	if err != nil {
		if !errors.Is(err, errStackBusy) {
			log.Warn("git poll", "err", err)
			var ge *gitstacks.Error
			if !errors.As(err, &ge) { // a failed fetch recorded itself
				s.recordGitFailure(ctx, src.Environment, src.Stack, "", &gitstacks.Error{Msg: deployError(err)})
			}
		}
		return
	}
	defer unlock()
	code, _, err := s.pullAndDeploy(ctx, stackRun{env: env, c: c, runner: runner, name: src.Stack, out: func(stacks.Line) {}}, gitPollUser)
	ok := err == nil && code == 0
	s.finishGitPull(ctx, env, src.Stack, pull, ok)
	status := http.StatusOK
	if !ok {
		status = http.StatusBadGateway
		log.Warn("git poll deploy", "code", code, "err", err)
	}
	if pull != nil {
		log.Info("git poll deploy", "commit", pull.commit, "ok", ok)
	}
	actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer acancel()
	if err := s.audit.Record(actx, audit.Entry{
		Time: s.now(), Username: gitPollUser, Environment: strconv.FormatInt(env.ID, 10),
		Action: "POST /api/v1/environments/{env}/stacks/{id}/git/pull", Target: src.Stack, Status: status,
	}); err != nil {
		log.Error("audit", "err", err)
	}
}
