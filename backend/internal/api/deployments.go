package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/deployments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

// deployWaitTimeout bounds how long ?wait=1 waits for a deployment run.
const deployWaitTimeout = 10 * time.Minute

type deploymentJSON struct {
	Name        string   `json:"name"`
	Environment int64    `json:"environment"`
	Stack       string   `json:"stack"`
	Description string   `json:"description"`
	Variables   []string `json:"variables"`
	// Health says when a run succeeded (deployments.Health).
	Health    deployments.Health `json:"health"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	// Role is the caller's role; "" for an admin who isn't a member.
	Role string `json:"role"`
	// Can lists the actions the caller may do, so the UI hides the others.
	Can []string `json:"can"`
}

var deploymentActions = []deployments.Action{
	deployments.ActionView, deployments.ActionDeploy, deployments.ActionEdit,
	deployments.ActionManageTokens, deployments.ActionManageMembers, deployments.ActionDelete,
}

func toDeploymentJSON(d deployments.Deployment, admin bool) deploymentJSON {
	can := []string{}
	for _, a := range deploymentActions {
		if admin || d.Role.Allows(a) {
			can = append(can, string(a))
		}
	}
	return deploymentJSON{
		Name: d.Name, Environment: d.Environment, Stack: d.Stack, Description: d.Description, Variables: d.Variables, Health: d.Health,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, Role: string(d.Role), Can: can,
	}
}

type deploymentMemberJSON struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type deployTokenJSON struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func toDeployTokenJSON(t deployments.Token) deployTokenJSON {
	scopes := make([]string, len(t.Scopes))
	for i, sc := range t.Scopes {
		scopes[i] = string(sc)
	}
	return deployTokenJSON{
		ID: t.ID, Name: t.Name, Prefix: t.Prefix, Scopes: scopes, CreatedBy: t.CreatedBy,
		CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt,
	}
}

type deploymentRunJSON struct {
	ID        int64             `json:"id"`
	Actor     string            `json:"actor"`
	ActorKind string            `json:"actor_kind"`
	Status    string            `json:"status"`
	Variables map[string]string `json:"variables"`
	// Previous are the values the run replaced, for maintainers and owners only.
	Previous    map[string]string `json:"previous"`
	QueuedAt    time.Time         `json:"queued_at"`
	StartedAt   *time.Time        `json:"started_at"`
	FinishedAt  *time.Time        `json:"finished_at"`
	ImageBefore string            `json:"image_before"`
	ImageAfter  string            `json:"image_after"`
	Error       string            `json:"error"`
	// Step is the last step the run reached: pulling, deploying, checking, rolling_back.
	Step     string   `json:"step"`
	Warnings []string `json:"warnings"`
}

// toDeploymentRunJSON leaves the replaced values out unless showPrevious: a variable
// may have held something only maintainers should see.
func toDeploymentRunJSON(r deployments.Run, showPrevious bool) deploymentRunJSON {
	prev := map[string]string{}
	if showPrevious {
		prev = r.Previous
	}
	warnings := r.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return deploymentRunJSON{
		ID: r.ID, Actor: r.ActorName, ActorKind: r.ActorKind, Status: string(r.Status), Variables: r.Variables, Previous: prev,
		QueuedAt: r.QueuedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, ImageBefore: r.ImageBefore, ImageAfter: r.ImageAfter, Error: r.Error,
		Step: string(r.Step), Warnings: warnings,
	}
}

func (s *server) deploymentsReady(w http.ResponseWriter) bool {
	if s.deployments == nil {
		s.writeError(w, http.StatusServiceUnavailable, "deployments aren't available")
		return false
	}
	return true
}

// deploymentActor is r's user as a deployments actor.
func (s *server) deploymentActor(r *http.Request) deployments.Actor {
	u, _ := userFrom(r.Context())
	return deployments.Actor{UserID: u.ID, Username: u.Username, Admin: isAdmin(u)}
}

func (s *server) deploymentError(w http.ResponseWriter, msg string, err error) {
	var inputErr *deployments.InputError
	switch {
	case errors.As(err, &inputErr):
		s.writeError(w, http.StatusBadRequest, inputErr.Msg)
	case errors.Is(err, deployments.ErrNotFound):
		s.writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, deployments.ErrForbidden), errors.Is(err, deployments.ErrScope):
		s.writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, deployments.ErrUnknownUser):
		s.writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, deployments.ErrTaken), errors.Is(err, deployments.ErrTokenExists),
		errors.Is(err, deployments.ErrLastOwner), errors.Is(err, deployments.ErrTooMany):
		s.writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, deployments.ErrClosed):
		s.writeError(w, http.StatusServiceUnavailable, "nudge is shutting down")
	default:
		s.internalError(w, msg, err)
	}
}

// authorizeDeployment returns the {name} deployment if r's user may do a with it, else
// answers.
func (s *server) authorizeDeployment(w http.ResponseWriter, r *http.Request, a deployments.Action) (deployments.Deployment, bool) {
	if !s.deploymentsReady(w) {
		return deployments.Deployment{}, false
	}
	name := r.PathValue("name")
	auditTarget(r, name)
	d, err := s.deployments.Authorize(r.Context(), s.deploymentActor(r), name, a)
	if err != nil {
		s.deploymentError(w, "deployment", err)
		return deployments.Deployment{}, false
	}
	auditEnvironment(r, strconv.FormatInt(d.Environment, 10))
	return d, true
}

func (s *server) listDeployments(w http.ResponseWriter, r *http.Request) {
	if !s.deploymentsReady(w) {
		return
	}
	var env int64
	if v := r.URL.Query().Get("environment"); v != "" {
		var err error
		if env, err = strconv.ParseInt(v, 10, 64); err != nil || env <= 0 {
			s.writeError(w, http.StatusBadRequest, "environment must be an environment id")
			return
		}
	}
	actor := s.deploymentActor(r)
	list, err := s.deployments.List(r.Context(), actor, env)
	if err != nil {
		s.deploymentError(w, "list deployments", err)
		return
	}
	out := make([]deploymentJSON, len(list))
	for i, d := range list {
		out[i] = toDeploymentJSON(d, actor.Admin)
	}
	s.writeJSON(w, http.StatusOK, out)
}

type createDeploymentRequest struct {
	Name        string             `json:"name"`
	Environment int64              `json:"environment"`
	Stack       string             `json:"stack"`
	Description string             `json:"description"`
	Variables   []string           `json:"variables"`
	Health      deployments.Health `json:"health"`
}

func (s *server) createDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.deploymentsReady(w) {
		return
	}
	var req createDeploymentRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	auditTarget(r, req.Name)
	env, err := s.envs.Get(r.Context(), req.Environment)
	if errors.Is(err, environments.ErrNotFound) || req.Environment <= 0 {
		s.writeError(w, http.StatusBadRequest, "environment not found")
		return
	}
	if err != nil {
		s.internalError(w, "create deployment", err)
		return
	}
	auditEnvironment(r, strconv.FormatInt(env.ID, 10))
	if !stacks.ValidName(req.Stack) {
		s.writeError(w, http.StatusBadRequest, "stack must name a stack")
		return
	}
	if _, err := s.stackDir(env.ID).Read(req.Stack); errors.Is(err, stacks.ErrNotFound) {
		s.writeError(w, http.StatusBadRequest, "the environment has no stack with that name")
		return
	} else if err != nil {
		s.internalError(w, "create deployment", err)
		return
	}
	actor := s.deploymentActor(r)
	d, err := s.deployments.Create(r.Context(), actor, deployments.Input{
		Name: req.Name, Environment: env.ID, Stack: req.Stack, Description: req.Description, Variables: req.Variables, Health: req.Health,
	})
	if err != nil {
		s.deploymentError(w, "create deployment", err)
		return
	}
	s.writeJSON(w, http.StatusCreated, toDeploymentJSON(d, actor.Admin))
}

func (s *server) getDeployment(w http.ResponseWriter, r *http.Request) {
	d, ok := s.authorizeDeployment(w, r, deployments.ActionView)
	if !ok {
		return
	}
	s.writeJSON(w, http.StatusOK, toDeploymentJSON(d, s.deploymentActor(r).Admin))
}

type changeDeploymentRequest struct {
	Description *string             `json:"description"`
	Variables   *[]string           `json:"variables"`
	Health      *deployments.Health `json:"health"`
}

func (s *server) changeDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.deploymentsReady(w) {
		return
	}
	var req changeDeploymentRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	auditTarget(r, name)
	u := deployments.Update{Description: req.Description, Health: req.Health}
	if req.Variables != nil {
		u.SetVariables, u.Variables = true, *req.Variables
	}
	actor := s.deploymentActor(r)
	d, err := s.deployments.Change(r.Context(), actor, name, u)
	if err != nil {
		s.deploymentError(w, "change deployment", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toDeploymentJSON(d, actor.Admin))
}

func (s *server) deleteDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.deploymentsReady(w) {
		return
	}
	name := r.PathValue("name")
	auditTarget(r, name)
	if err := s.deployments.Delete(r.Context(), s.deploymentActor(r), name); err != nil {
		s.deploymentError(w, "delete deployment", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listDeploymentMembers(w http.ResponseWriter, r *http.Request) {
	if !s.deploymentsReady(w) {
		return
	}
	members, err := s.deployments.Members(r.Context(), s.deploymentActor(r), r.PathValue("name"))
	if err != nil {
		s.deploymentError(w, "list deployment members", err)
		return
	}
	out := make([]deploymentMemberJSON, len(members))
	for i, m := range members {
		out[i] = deploymentMemberJSON{UserID: m.UserID, Username: m.Username, Role: string(m.Role)}
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *server) setDeploymentMember(w http.ResponseWriter, r *http.Request) {
	if !s.deploymentsReady(w) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	auditTarget(r, name+"/"+req.Username)
	m, err := s.deployments.SetMember(r.Context(), s.deploymentActor(r), name, req.Username, deployments.Role(req.Role))
	if err != nil {
		s.deploymentError(w, "set deployment member", err)
		return
	}
	s.writeJSON(w, http.StatusOK, deploymentMemberJSON{UserID: m.UserID, Username: m.Username, Role: string(m.Role)})
}

func (s *server) removeDeploymentMember(w http.ResponseWriter, r *http.Request) {
	if !s.deploymentsReady(w) {
		return
	}
	name := r.PathValue("name")
	user, err := strconv.ParseInt(r.PathValue("user"), 10, 64)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not found")
		return
	}
	auditTarget(r, name+"/"+r.PathValue("user"))
	if err := s.deployments.RemoveMember(r.Context(), s.deploymentActor(r), name, user); err != nil {
		s.deploymentError(w, "remove deployment member", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listDeployTokens(w http.ResponseWriter, r *http.Request) {
	if !s.deploymentsReady(w) {
		return
	}
	tokens, err := s.deployments.Tokens(r.Context(), s.deploymentActor(r), r.PathValue("name"))
	if err != nil {
		s.deploymentError(w, "list deploy tokens", err)
		return
	}
	out := make([]deployTokenJSON, len(tokens))
	for i, t := range tokens {
		out[i] = toDeployTokenJSON(t)
	}
	s.writeJSON(w, http.StatusOK, out)
}

type createDeployTokenResponse struct {
	deployTokenJSON
	// Token is the token itself, shown only this once.
	Token string `json:"token"`
}

func (s *server) createDeployToken(w http.ResponseWriter, r *http.Request) {
	if !s.deploymentsReady(w) {
		return
	}
	var req struct {
		Name      string     `json:"name"`
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	auditTarget(r, name+"/"+req.Name)
	in := deployments.TokenInput{Name: req.Name, ExpiresAt: req.ExpiresAt}
	for _, sc := range req.Scopes {
		in.Scopes = append(in.Scopes, deployments.Scope(sc))
	}
	t, secret, err := s.deployments.CreateToken(r.Context(), s.deploymentActor(r), name, in)
	if err != nil {
		s.deploymentError(w, "create deploy token", err)
		return
	}
	s.writeJSON(w, http.StatusCreated, createDeployTokenResponse{deployTokenJSON: toDeployTokenJSON(t), Token: secret})
}

func (s *server) deleteDeployToken(w http.ResponseWriter, r *http.Request) {
	if !s.deploymentsReady(w) {
		return
	}
	name := r.PathValue("name")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not found")
		return
	}
	auditTarget(r, name+"/"+r.PathValue("id"))
	if err := s.deployments.DeleteToken(r.Context(), s.deploymentActor(r), name, id); err != nil {
		s.deploymentError(w, "delete deploy token", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deployCaller is who asks for a deploy or its runs: a user, or a deploy token.
type deployCaller struct {
	d   deployments.Deployment
	req deployments.Request
	// user is the user the deploy acts as (a token's creator), for the update history.
	user string
	// showPrevious lets the caller see the values runs replaced.
	showPrevious bool
}

// userDeployCaller authorizes r's user to do a with the {name} deployment.
func (s *server) userDeployCaller(w http.ResponseWriter, r *http.Request, a deployments.Action) (deployCaller, bool) {
	d, ok := s.authorizeDeployment(w, r, a)
	if !ok {
		return deployCaller{}, false
	}
	u, _ := userFrom(r.Context())
	return deployCaller{
		d: d, req: deployments.Request{ActorKind: deployments.ActorUser, ActorName: u.Username}, user: u.Username,
		showPrevious: isAdmin(u) || d.Role.Allows(deployments.ActionEdit),
	}, true
}

func (s *server) startDeploymentRun(w http.ResponseWriter, r *http.Request) {
	if c, ok := s.userDeployCaller(w, r, deployments.ActionDeploy); ok {
		s.serveDeploy(w, r, c)
	}
}

func (s *server) listDeploymentRuns(w http.ResponseWriter, r *http.Request) {
	if c, ok := s.userDeployCaller(w, r, deployments.ActionView); ok {
		s.serveRuns(w, r, c)
	}
}

func (s *server) getDeploymentRun(w http.ResponseWriter, r *http.Request) {
	if c, ok := s.userDeployCaller(w, r, deployments.ActionView); ok {
		s.serveRun(w, r, c)
	}
}

// serveDeploy queues a run of c's deployment: 202 with the run, or with ?wait=1 the
// finished run (200 succeeded, 502 failed or rolled back, 409 superseded), or 202 if it
// still runs after deployWaitTimeout; ?stream=1 streams its progress (streamDeploy).
func (s *server) serveDeploy(w http.ResponseWriter, r *http.Request, c deployCaller) {
	var req struct {
		Variables map[string]string `json:"variables"`
	}
	if r.ContentLength != 0 && !s.decodeJSON(w, r, &req) {
		return
	}
	wait, ok := s.queryBool(w, r, "wait")
	if !ok {
		return
	}
	stream, ok := s.queryBool(w, r, "stream")
	if !ok {
		return
	}
	c.req.Variables = req.Variables
	user := c.user
	ticket, err := s.deployments.Deploy(r.Context(), c.d, c.req, func(ctx context.Context, d deployments.Deployment, run deployments.Run, report deployments.Report) deployments.Outcome {
		return s.deployDeployment(ctx, d, run, user, report)
	})
	if err != nil {
		s.deploymentError(w, "deploy", err)
		return
	}
	s.log.Info("deployment run queued", "deployment", c.d.Name, "run", ticket.RunID, "actor", c.req.ActorKind+":"+c.req.ActorName, "ip", clientIP(r))
	if stream {
		s.streamDeploy(w, r, c, ticket)
		return
	}
	if !wait {
		run, err := s.deployments.Run(r.Context(), c.d.ID, ticket.RunID)
		if err != nil {
			s.deploymentError(w, "deployment run", err)
			return
		}
		s.writeJSON(w, http.StatusAccepted, toDeploymentRunJSON(run, c.showPrevious))
		return
	}
	run, finished, err := s.deployments.Wait(r.Context(), c.d, ticket, deployWaitTimeout)
	switch {
	case r.Context().Err() != nil:
		// The client went away; the run goes on.
		return
	case err != nil:
		s.deploymentError(w, "deployment run", err)
	case !finished:
		s.writeJSON(w, http.StatusAccepted, toDeploymentRunJSON(run, c.showPrevious))
	case run.Status == deployments.StatusSucceeded:
		s.writeJSON(w, http.StatusOK, toDeploymentRunJSON(run, c.showPrevious))
	case run.Status == deployments.StatusSuperseded:
		s.writeJSON(w, http.StatusConflict, toDeploymentRunJSON(run, c.showPrevious))
	default:
		s.writeJSON(w, http.StatusBadGateway, toDeploymentRunJSON(run, c.showPrevious))
	}
}

func (s *server) serveRuns(w http.ResponseWriter, r *http.Request, c deployCaller) {
	runs, err := s.deployments.Runs(r.Context(), c.d.ID)
	if err != nil {
		s.deploymentError(w, "list deployment runs", err)
		return
	}
	out := make([]deploymentRunJSON, len(runs))
	for i, run := range runs {
		out[i] = toDeploymentRunJSON(run, c.showPrevious)
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *server) serveRun(w http.ResponseWriter, r *http.Request, c deployCaller) {
	id, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not found")
		return
	}
	run, err := s.deployments.Run(r.Context(), c.d.ID, id)
	if err != nil {
		s.deploymentError(w, "deployment run", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toDeploymentRunJSON(run, c.showPrevious))
}

// restoreEnv puts back the .env a failed run replaced; the caller holds the stack.
func (s *server) restoreEnv(env environments.Environment, d deployments.Deployment, run deployments.Run, original string) {
	dir := s.stackDir(env.ID)
	f, err := dir.Read(d.Stack)
	if err == nil {
		f.Env = original
		err = dir.Update(d.Stack, f)
	}
	if err != nil {
		s.log.Error("deployment run: restore .env", "deployment", d.Name, "run", run.ID, "err", err)
	}
}

// deployTokenRoutes are the routes a deploy token may call; everything else refuses it.
func (s *server) deployTokenRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/deployments/{name}/deploys", s.tokenRoute(deployments.ScopeDeploy, s.serveDeploy))
	mux.HandleFunc("GET /api/v1/deployments/{name}/deploys", s.tokenRoute(deployments.ScopeRunsRead, s.serveRuns))
	mux.HandleFunc("GET /api/v1/deployments/{name}/deploys/{run}", s.tokenRoute(deployments.ScopeRunsRead, s.serveRun))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		s.writeError(w, http.StatusForbidden, "deploy tokens only work on their deployment's deploys")
	})
	return mux
}

// tokenRoute authenticates the request's deploy token for the {name} deployment and
// scope sc, then serves it with next. Failed checks count against the client IP's
// failed-login limit, like a deploy webhook's secret.
func (s *server) tokenRoute(sc deployments.Scope, next func(http.ResponseWriter, *http.Request, deployCaller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.deploymentsReady(w) {
			return
		}
		ip := clientIP(r)
		succeeded, err := s.auth.Attempt(ip)
		var limited *auth.RateLimitedError
		if errors.As(err, &limited) {
			s.log.Warn("deploy token rate limited", "ip", ip)
			retryAfter(w, limited.RetryAfter)
			s.writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
			return
		}
		secret, _ := bearerToken(r)
		name := r.PathValue("name")
		d, t, err := s.deployments.AuthenticateToken(r.Context(), name, secret, sc)
		switch {
		case errors.Is(err, deployments.ErrInvalidToken):
			s.log.Warn("deploy token refused", "ip", ip)
			s.writeError(w, http.StatusUnauthorized, "authentication required")
			return
		case errors.Is(err, deployments.ErrScope):
			succeeded()
			s.writeError(w, http.StatusForbidden, err.Error())
			return
		case err != nil:
			succeeded()
			s.internalError(w, "authenticate deploy token", err)
			return
		}
		succeeded()
		// A token acts for its creator, so it does no more than they still may.
		need := deployments.ActionView
		if sc == deployments.ScopeDeploy {
			need = deployments.ActionDeploy
		}
		if !t.CreatorAdmin && !t.CreatorRole.Allows(need) {
			s.writeError(w, http.StatusForbidden, "the token's creator may no longer do that on this deployment")
			return
		}
		auditUser(r, auth.User{Username: t.CreatedBy, Token: "deploy:" + t.Name})
		auditTarget(r, d.Name)
		auditEnvironment(r, strconv.FormatInt(d.Environment, 10))
		next(w, r, deployCaller{d: d, req: deployments.Request{ActorKind: deployments.ActorToken, ActorName: t.Name}, user: t.CreatedBy})
	}
}

// isDeployToken reports whether r carries a deploy token rather than an API token.
func isDeployToken(r *http.Request) bool {
	token, ok := bearerToken(r)
	return ok && strings.HasPrefix(token, deployments.TokenPrefix)
}
