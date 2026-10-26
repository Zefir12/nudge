// Package api is nudge's HTTP API under /api/v1, the only interface the web UI uses.
// Everything else is handed to the web UI handler.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"

	"git.zefirlabs.net/zefir/nudge/backend/internal/audit"
	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/deployhooks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/events"
	"git.zefirlabs.net/zefir/nudge/backend/internal/notify"
	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
	"git.zefirlabs.net/zefir/nudge/backend/internal/registryclient"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/updates"
)

// Options are the API's dependencies.
type Options struct {
	// Context ends long-lived streams (Docker events) at shutdown, which
	// http.Server.Shutdown leaves running; nil means never.
	Context context.Context
	Log     *slog.Logger
	Version string
	Auth    *auth.Service
	Envs    *environments.Service
	// Registries has the credentials image pulls use.
	Registries *registries.Service
	// RegistryClient browses registries over HTTP; nil means a registryclient.Client.
	RegistryClient RegistryBrowser
	// Admin says whether a user may do admin-only actions (deleting registry tags,
	// managing every user's deploy webhooks); nil means every user is an admin, as nudge
	// has one user until roles exist.
	Admin func(auth.User) bool
	// Audit records every write.
	Audit *audit.Service
	// StacksDir holds every environment's stacks as <dir>/<environment ID>/<name>/.
	StacksDir string
	// StackRunner returns the runner that deploys env's stacks, nil if env can't
	// deploy them.
	StackRunner func(env environments.Environment) stacks.Runner
	// StackChecker returns compose's complaints about stack files, none if they're valid;
	// nil means stacks can't be checked.
	StackChecker func(ctx context.Context, f stacks.Files) ([]string, error)
	// Updates checks images for newer versions; nil means update checks aren't available.
	Updates *updates.Service
	// Webhooks are the notification targets; nil means notifications aren't available.
	Webhooks *notify.Service
	// DeployHooks are the deploy webhooks; nil means they aren't available.
	DeployHooks *deployhooks.Service
	// Events is the Docker events hub, shared with the notification watcher; nil means
	// the API makes its own.
	Events *events.Hub
	// Web serves every path outside /api/.
	Web http.Handler
	// PublicURL is the URL agents reach the server at (scheme://host[:port]); empty means
	// guess it from each request.
	PublicURL string
	// SecureCookies marks the session cookie Secure on every response, not only on
	// requests that came over HTTPS (set when nudge serves TLS or PublicURL is https).
	SecureCookies bool
	// TrustedProxies are the reverse proxies whose X-Forwarded-For (client IP for the
	// login limiter and logs) and X-Forwarded-Proto are believed; empty means none.
	TrustedProxies []netip.Prefix
}

// publicRoutes need no session; every other pattern, including the /api/ catch-all,
// does, so a route added without thought is protected.
var publicRoutes = map[string]bool{
	"GET /api/v1/health":      true,
	"GET /api/v1/setup":       true,
	"POST /api/v1/setup":      true,
	"POST /api/v1/auth/login": true,
	// Logout clears the cookie even when the session has already expired.
	"POST /api/v1/auth/logout": true,
	// Agents enroll with a join token and connect with their ID and secret instead of a
	// session.
	"POST /api/v1/agents/enroll": true,
	"GET /api/v1/agents/connect": true,
	// A deploy webhook's secret is the credential (a bearer token or the key of the
	// body's signature), checked by the handler.
	"POST /api/v1/hooks/{id}": true,
}

// sessionOnlyRoutes refuse API tokens (403) and need a session: they mint credentials
// that would outlive the token's revocation, so a leaked token can't leave a way back in.
// The refusal is audited with the token's name.
var sessionOnlyRoutes = map[string]bool{
	"POST /api/v1/tokens": true,
}

// New returns the server's root handler: the API plus the web UI.
func New(opts Options) http.Handler {
	s := &server{
		log: opts.Log, version: opts.Version, auth: opts.Auth, envs: opts.Envs, registries: opts.Registries, audit: opts.Audit, updates: opts.Updates,
		webhooks: opts.Webhooks, events: opts.Events, deployHooks: opts.DeployHooks, registryClient: opts.RegistryClient, isAdmin: opts.Admin,
		publicURL: opts.PublicURL, secureCookies: opts.SecureCookies, recreating: map[string]bool{},
		stacksDir: opts.StacksDir, stackRunner: opts.StackRunner, stackChecker: opts.StackChecker, stackBusy: map[string]bool{},
		checks: make(chan struct{}, maxStackChecks),
		done:   context.Background(),
	}
	if s.events == nil {
		s.events = events.New(events.Options{Context: opts.Context, Log: opts.Log})
	}
	if s.registryClient == nil {
		s.registryClient = registryclient.New(registryclient.Options{})
	}
	if s.isAdmin == nil {
		s.isAdmin = func(auth.User) bool { return true }
	}
	if opts.Context != nil {
		s.done = opts.Context
	}
	if s.stackRunner == nil {
		s.stackRunner = func(environments.Environment) stacks.Runner { return nil }
	}
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/health", s.health)
	api.HandleFunc("GET /api/v1/setup", s.setupStatus)
	api.HandleFunc("POST /api/v1/setup", s.setup)
	api.HandleFunc("POST /api/v1/auth/login", s.login)
	api.HandleFunc("POST /api/v1/auth/logout", s.logout)
	api.HandleFunc("GET /api/v1/auth/me", s.me)
	api.HandleFunc("GET /api/v1/tokens", s.listTokens)
	api.HandleFunc("POST /api/v1/tokens", s.createToken)
	api.HandleFunc("DELETE /api/v1/tokens/{id}", s.deleteToken)
	api.HandleFunc("GET /api/v1/audit", s.listAudit)
	api.HandleFunc("GET /api/v1/registries", s.listRegistries)
	api.HandleFunc("POST /api/v1/registries", s.createRegistry)
	api.HandleFunc("GET /api/v1/registries/{id}", s.getRegistry)
	api.HandleFunc("PUT /api/v1/registries/{id}", s.updateRegistry)
	api.HandleFunc("DELETE /api/v1/registries/{id}", s.deleteRegistry)
	api.HandleFunc("GET /api/v1/registries/{id}/repositories", s.listRegistryRepositories)
	api.HandleFunc("GET /api/v1/registries/{id}/tags", s.listRegistryTags)
	api.HandleFunc("DELETE /api/v1/registries/{id}/tags", s.deleteRegistryTag)
	api.HandleFunc("GET /api/v1/registries/{id}/manifest", s.getRegistryManifest)
	api.HandleFunc("GET /api/v1/webhooks", s.listWebhooks)
	api.HandleFunc("POST /api/v1/webhooks", s.createWebhook)
	api.HandleFunc("GET /api/v1/webhooks/{id}", s.getWebhook)
	api.HandleFunc("PUT /api/v1/webhooks/{id}", s.updateWebhook)
	api.HandleFunc("DELETE /api/v1/webhooks/{id}", s.deleteWebhook)
	api.HandleFunc("POST /api/v1/webhooks/{id}/test", s.testWebhook)
	api.HandleFunc("GET /api/v1/environments", s.listEnvironments)
	api.HandleFunc("POST /api/v1/environments", s.createEnvironment)
	api.HandleFunc("GET /api/v1/environments/{env}", s.getEnvironment)
	api.HandleFunc("DELETE /api/v1/environments/{env}", s.deleteEnvironment)
	api.HandleFunc("POST /api/v1/environments/{env}/join-tokens", s.createJoinToken)
	api.HandleFunc("DELETE /api/v1/environments/{env}/credentials", s.revokeAgent)
	api.HandleFunc("POST /api/v1/agents/enroll", s.enrollAgent)
	api.HandleFunc("GET /api/v1/agents/connect", s.connectAgent)
	api.HandleFunc("GET /api/v1/environments/{env}/containers", s.listContainers)
	api.HandleFunc("POST /api/v1/environments/{env}/containers", s.createContainer)
	api.HandleFunc("GET /api/v1/environments/{env}/containers/{id}", s.getContainer)
	api.HandleFunc("GET /api/v1/environments/{env}/containers/{id}/spec", s.getContainerSpec)
	api.HandleFunc("POST /api/v1/environments/{env}/containers/{id}/recreate", s.recreateContainer)
	api.HandleFunc("POST /api/v1/environments/{env}/containers/{id}/update", s.updateContainer)
	api.HandleFunc("DELETE /api/v1/environments/{env}/containers/{id}", s.removeContainer)
	api.HandleFunc("POST /api/v1/environments/{env}/containers/{id}/start", s.startContainer)
	api.HandleFunc("POST /api/v1/environments/{env}/containers/{id}/stop", s.stopContainer)
	api.HandleFunc("POST /api/v1/environments/{env}/containers/{id}/restart", s.restartContainer)
	api.HandleFunc("POST /api/v1/environments/{env}/containers/{id}/kill", s.killContainer)
	api.HandleFunc("GET /api/v1/environments/{env}/containers/{id}/logs", s.containerLogs)
	api.HandleFunc("GET /api/v1/environments/{env}/containers/{id}/stats", s.containerStats)
	api.HandleFunc("GET /api/v1/environments/{env}/containers/{id}/exec", s.containerExec)
	api.HandleFunc("GET /api/v1/environments/{env}/stats", s.environmentStats)
	api.HandleFunc("GET /api/v1/environments/{env}/events", s.environmentEvents)
	api.HandleFunc("GET /api/v1/environments/{env}/images", s.listImages)
	api.HandleFunc("POST /api/v1/environments/{env}/images/pull", s.pullImage)
	api.HandleFunc("POST /api/v1/environments/{env}/images/prune", s.pruneImages)
	api.HandleFunc("GET /api/v1/environments/{env}/images/{id}", s.getImage)
	api.HandleFunc("DELETE /api/v1/environments/{env}/images/{id}", s.removeImage)
	api.HandleFunc("GET /api/v1/environments/{env}/volumes", s.listVolumes)
	api.HandleFunc("POST /api/v1/environments/{env}/volumes", s.createVolume)
	api.HandleFunc("POST /api/v1/environments/{env}/volumes/prune", s.pruneVolumes)
	api.HandleFunc("GET /api/v1/environments/{env}/volumes/{id}", s.getVolume)
	api.HandleFunc("DELETE /api/v1/environments/{env}/volumes/{id}", s.removeVolume)
	api.HandleFunc("GET /api/v1/environments/{env}/networks", s.listNetworks)
	api.HandleFunc("POST /api/v1/environments/{env}/networks", s.createNetwork)
	api.HandleFunc("POST /api/v1/environments/{env}/networks/prune", s.pruneNetworks)
	api.HandleFunc("GET /api/v1/environments/{env}/networks/{id}", s.getNetwork)
	api.HandleFunc("DELETE /api/v1/environments/{env}/networks/{id}", s.removeNetwork)
	api.HandleFunc("POST /api/v1/stacks/check", s.checkStack)
	api.HandleFunc("GET /api/v1/environments/{env}/stacks", s.listStacks)
	api.HandleFunc("POST /api/v1/environments/{env}/stacks", s.createStack)
	api.HandleFunc("GET /api/v1/environments/{env}/stacks/{id}", s.getStack)
	api.HandleFunc("PUT /api/v1/environments/{env}/stacks/{id}", s.updateStack)
	api.HandleFunc("DELETE /api/v1/environments/{env}/stacks/{id}", s.removeStack)
	api.HandleFunc("POST /api/v1/environments/{env}/stacks/{id}/up", s.upStack)
	api.HandleFunc("POST /api/v1/environments/{env}/stacks/{id}/down", s.downStack)
	api.HandleFunc("POST /api/v1/environments/{env}/stacks/{id}/update", s.updateStackImages)
	api.HandleFunc("GET /api/v1/environments/{env}/updates", s.listUpdates)
	api.HandleFunc("POST /api/v1/environments/{env}/updates/check", s.checkUpdates)
	api.HandleFunc("GET /api/v1/environments/{env}/updates/history", s.listUpdateHistory)
	api.HandleFunc("GET /api/v1/environments/{env}/webhooks", s.listDeployHooks)
	api.HandleFunc("POST /api/v1/environments/{env}/webhooks", s.createDeployHook)
	api.HandleFunc("DELETE /api/v1/environments/{env}/webhooks/{id}", s.deleteDeployHook)
	api.HandleFunc("POST /api/v1/environments/{env}/webhooks/{id}/secret", s.rotateDeployHookSecret)
	api.HandleFunc("GET /api/v1/environments/{env}/webhooks/{id}/runs", s.listDeployHookRuns)
	api.HandleFunc("POST /api/v1/hooks/{id}", s.triggerDeployHook)
	api.HandleFunc("/api/", s.notFound)

	// tokenRefused answers the session-only routes for token requests, through a mux so
	// the audit entry gets the route and its path values.
	s.tokenRefused = http.NewServeMux()
	for pattern := range sessionOnlyRoutes {
		s.tokenRefused.HandleFunc(pattern, s.refuseToken)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", s.protect(api))
	mux.Handle("/", opts.Web)
	return proxies(opts.TrustedProxies).handler(mux)
}

type server struct {
	log     *slog.Logger
	version string
	auth    *auth.Service
	envs    *environments.Service

	registries     *registries.Service
	registryClient RegistryBrowser
	// isAdmin gates admin-only actions (Options.Admin), such as managing every user's
	// deploy webhooks.
	isAdmin  func(auth.User) bool
	audit    *audit.Service
	updates  *updates.Service
	webhooks *notify.Service
	// deployHooks are the deploy webhooks.
	deployHooks *deployhooks.Service
	// done ends at shutdown (Options.Context).
	done context.Context
	// events fans each environment's Docker events out to the clients watching it.
	events *events.Hub

	publicURL     string
	secureCookies bool

	stacksDir    string
	stackRunner  func(env environments.Environment) stacks.Runner
	stackChecker func(ctx context.Context, f stacks.Files) ([]string, error)
	// checks holds a slot per running stack check.
	checks chan struct{}
	// tokenRefused serves sessionOnlyRoutes to API tokens.
	tokenRefused *http.ServeMux

	mu sync.Mutex
	// recreating holds "<env>/<container ID>" of the recreates running now.
	recreating map[string]bool
	// stackBusy holds stackKey of the stacks being changed or deployed now.
	stackBusy map[string]bool
}

// protect requires a session or an API token for every route outside publicRoutes.
// Requests with an Authorization header are authenticated by it alone (a cookie is
// ignored) and skip the cross-origin check: browsers can't add that header to a
// cross-origin request without a CORS preflight, which nudge never allows (this holds for
// public routes too, which then skip both checks). All other
// requests have cross-origin writes refused (Sec-Fetch-Site, else Origin against Host).
func (s *server) protect(api *http.ServeMux) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.writeError(w, http.StatusForbidden, "cross-origin request refused")
	}))
	withSession := cop.Handler(s.authenticate(api, false, func(r *http.Request) (auth.User, error) {
		return s.auth.SessionUser(r.Context(), sessionToken(r))
	}))
	withToken := s.authenticate(api, true, func(r *http.Request) (auth.User, error) {
		token, ok := bearerToken(r)
		if !ok {
			return auth.User{}, auth.ErrNoSession
		}
		return s.auth.TokenUser(r.Context(), token)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			withToken.ServeHTTP(w, r)
			return
		}
		withSession.ServeHTTP(w, r)
	})
}

// authenticate serves public routes as they are and every other route only to the user
// that lookup finds. With token set (lookup checks an API token) sessionOnlyRoutes are
// refused.
func (s *server) authenticate(api *http.ServeMux, token bool, lookup func(*http.Request) (auth.User, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := api.Handler(r)
		if publicRoutes[pattern] {
			s.serveAudited(api, w, r)
			return
		}
		u, err := lookup(r)
		if errors.Is(err, auth.ErrNoSession) {
			s.writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if err != nil {
			s.internalError(w, "authenticate", err)
			return
		}
		r = r.WithContext(withUser(r.Context(), u))
		if token && sessionOnlyRoutes[pattern] {
			s.serveAudited(s.tokenRefused, w, r)
			return
		}
		s.serveAudited(api, w, r)
	})
}

func (s *server) refuseToken(w http.ResponseWriter, _ *http.Request) {
	s.writeError(w, http.StatusForbidden, "API tokens can't do this; sign in to the web UI")
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: s.version})
}

func (s *server) notFound(w http.ResponseWriter, _ *http.Request) {
	s.writeError(w, http.StatusNotFound, "not found")
}

// errorResponse is the body of every API error.
type errorResponse struct {
	Error string `json:"error"`
}

// internalError logs err and answers 500 without details.
func (s *server) internalError(w http.ResponseWriter, msg string, err error) {
	s.log.Error(msg, "err", err)
	s.writeError(w, http.StatusInternalServerError, "internal error")
}

// maxBodyBytes bounds JSON request bodies.
const maxBodyBytes = 64 << 10

// decodeJSON reads r's body into v, answering 400 itself when it fails.
func (s *server) decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %s", jsonErrorKind(err)))
		return false
	}
	return true
}

// jsonErrorKind describes a decode error without echoing the body.
func jsonErrorKind(err error) string {
	var tooBig *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &tooBig):
		return "too large"
	case errors.As(err, &typeErr):
		return fmt.Sprintf("field %q has the wrong type", typeErr.Field)
	default:
		return "malformed"
	}
}

func (s *server) writeError(w http.ResponseWriter, code int, msg string) {
	s.writeJSON(w, code, errorResponse{Error: msg})
}

func (s *server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Warn("writing response", "err", err)
	}
}
