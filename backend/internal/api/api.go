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
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/allowances"
	"git.zefirlabs.net/zefir/nudge/backend/internal/audit"
	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/backup"
	"git.zefirlabs.net/zefir/nudge/backend/internal/deployhooks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/deployments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/events"
	"git.zefirlabs.net/zefir/nudge/backend/internal/notify"
	"git.zefirlabs.net/zefir/nudge/backend/internal/oidc"
	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
	"git.zefirlabs.net/zefir/nudge/backend/internal/registryclient"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/updates"
	"git.zefirlabs.net/zefir/nudge/backend/internal/usage"
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
	// Deployments are the deployments; nil means they aren't available.
	Deployments *deployments.Service
	// Usage keeps the measured volume sizes; nil means sizes aren't available.
	Usage *usage.Service
	// Backups writes backups; nil means none can be downloaded.
	Backups *backup.Service
	// Allowances are the users' allowances, checked before a user's container is
	// created, recreated, updated or started.
	Allowances *allowances.Service
	// Tenant is the host-side hardening of users' containers (cgroup slice, runtime,
	// disk limit); the zero value adds none.
	Tenant TenantHosting
	// UserRegistries are the registry hosts users' pulls may reach, "*" for any; nil
	// means DefaultUserRegistries (pull_registry.go).
	UserRegistries []string
	// DeniedLabels are label prefixes (lower case) a user's container may not carry, from
	// its spec or its image: labels that steer tools on the host such as reverse proxies
	// (traefik.) or updaters. Empty denies none.
	DeniedLabels []string
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
	// After is time.After outside tests: the deployments' stability window and the
	// keepalives of their progress streams.
	After func(time.Duration) <-chan time.Time
	// Now is time.Now outside tests: the age of the environments' usage summaries.
	Now func() time.Time
	// OIDC signs users in through the SSO provider; nil means SSO is off.
	OIDC *oidc.Client
	// OIDCName labels the SSO button on the login page.
	OIDCName string
	// NoPasswordLogin hides the password form; the auth service refuses everyone but the
	// break-glass admin.
	NoPasswordLogin bool
	// Self is nudge's own container (stacks.SelfID) and DockerHost how the server reaches
	// the local environment's daemon: what an update of the server replaces, and what the
	// generic recreate, update and stack runs refuse to touch.
	Self       stacks.Self
	DockerHost string
	// AgentSelf asks an agent environment's agent for its own container and Docker host
	// (selfupdate.RemoteSelf); nil means agents can't be updated from the server.
	AgentSelf func(ctx context.Context, env environments.Environment) (stacks.Self, string, error)
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
	// The login page asks which ways to sign in it offers; the SSO login starts and ends
	// before there is a session (the callback checks its state and binding cookie).
	"GET /api/v1/auth/methods":       true,
	"GET /api/v1/auth/oidc/login":    true,
	"GET /api/v1/auth/oidc/callback": true,
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
	"POST /api/v1/tokens":                    true,
	"POST /api/v1/deployments/{name}/tokens": true,
	// A user's password is a credential too; a token mustn't be able to take over the
	// account by changing it.
	"POST /api/v1/auth/password": true,
	"POST /api/v1/users":         true,
	"PATCH /api/v1/users/{id}":   true,
	// A backup holds the key to the stored registry passwords and every credential hash.
	"POST /api/v1/backup": true,
	// A join token enrolls an agent, whose credentials stay valid until revoked.
	"POST /api/v1/environments":                   true,
	"POST /api/v1/environments/{env}/join-tokens": true,
	// A deploy webhook's secret triggers deploys as its creator.
	"POST /api/v1/environments/{env}/webhooks":             true,
	"POST /api/v1/environments/{env}/webhooks/{id}/secret": true,
}

// userRoutes are the routes open to users; every other authenticated route is for admins
// only (403 for a user's session or API token), so a new route is admin-only until it is
// listed here. Handlers of these routes check what the user may do with the object
// (deployments.Authorize).
var userRoutes = map[string]bool{
	// The catch-all only answers 404.
	"/api/":                    true,
	"GET /api/v1/auth/me":      true,
	"POST /api/v1/auth/logout": true,
	// Their own password.
	"POST /api/v1/auth/password": true,
	// Their own API tokens.
	"GET /api/v1/tokens":         true,
	"POST /api/v1/tokens":        true,
	"DELETE /api/v1/tokens/{id}": true,
	// Deployments they are a member of; creating one is for admins.
	"GET /api/v1/deployments":                          true,
	"GET /api/v1/deployments/{name}":                   true,
	"PATCH /api/v1/deployments/{name}":                 true,
	"DELETE /api/v1/deployments/{name}":                true,
	"GET /api/v1/deployments/{name}/members":           true,
	"POST /api/v1/deployments/{name}/members":          true,
	"DELETE /api/v1/deployments/{name}/members/{user}": true,
	"GET /api/v1/deployments/{name}/tokens":            true,
	"POST /api/v1/deployments/{name}/tokens":           true,
	"DELETE /api/v1/deployments/{name}/tokens/{id}":    true,
	"POST /api/v1/deployments/{name}/deploys":          true,
	"GET /api/v1/deployments/{name}/deploys":           true,
	"GET /api/v1/deployments/{name}/deploys/{run}":     true,
	// The environments an admin gave them, and in those only the containers, volumes and
	// networks they own (scope): someone else's are 404. Creating and recreating containers
	// with the admin spec, images, stacks and prunes stay with admins.
	"GET /api/v1/environments":                                true,
	"GET /api/v1/environments/{env}":                          true,
	"GET /api/v1/environments/{env}/containers":               true,
	"GET /api/v1/environments/{env}/containers/{id}":          true,
	"POST /api/v1/environments/{env}/containers/{id}/update":  true,
	"DELETE /api/v1/environments/{env}/containers/{id}":       true,
	"POST /api/v1/environments/{env}/containers/{id}/start":   true,
	"POST /api/v1/environments/{env}/containers/{id}/stop":    true,
	"POST /api/v1/environments/{env}/containers/{id}/restart": true,
	"POST /api/v1/environments/{env}/containers/{id}/kill":    true,
	"GET /api/v1/environments/{env}/containers/{id}/logs":     true,
	"GET /api/v1/environments/{env}/containers/{id}/stats":    true,
	"GET /api/v1/environments/{env}/containers/{id}/exec":     true,
	"GET /api/v1/environments/{env}/stats":                    true,
	"GET /api/v1/environments/{env}/stats/summary":            true,
	// Their own allowance; setting one is for admins.
	"GET /api/v1/environments/{env}/allowance":        true,
	"GET /api/v1/environments/{env}/events":           true,
	"GET /api/v1/environments/{env}/volumes":          true,
	"GET /api/v1/environments/{env}/volumes/{id}":     true,
	"DELETE /api/v1/environments/{env}/volumes/{id}":  true,
	"GET /api/v1/environments/{env}/networks":         true,
	"GET /api/v1/environments/{env}/networks/{id}":    true,
	"DELETE /api/v1/environments/{env}/networks/{id}": true,
	// Their own containers, built by the server from the strict tenant spec (tenant.go).
	"POST /api/v1/environments/{env}/user-containers":               true,
	"GET /api/v1/environments/{env}/user-containers/{id}/spec":      true,
	"POST /api/v1/environments/{env}/user-containers/{id}/recreate": true,
	// Their own networks and volumes: bridge and local only, named with their prefix.
	"POST /api/v1/environments/{env}/volumes":  true,
	"POST /api/v1/environments/{env}/networks": true,
}

// route is one API route: its mux pattern and the server method that serves it.
type route struct {
	pattern string
	handler func(*server, http.ResponseWriter, *http.Request)
}

// routes are every route the API serves.
var routes = []route{
	{"GET /api/v1/health", (*server).health},
	{"GET /api/v1/setup", (*server).setupStatus},
	{"POST /api/v1/setup", (*server).setup},
	{"POST /api/v1/auth/login", (*server).login},
	{"POST /api/v1/auth/logout", (*server).logout},
	{"POST /api/v1/auth/password", (*server).changePassword},
	{"GET /api/v1/auth/me", (*server).me},
	{"GET /api/v1/auth/methods", (*server).authMethods},
	{"GET /api/v1/auth/oidc/login", (*server).oidcLogin},
	{"GET /api/v1/auth/oidc/callback", (*server).oidcCallback},
	{"GET /api/v1/tokens", (*server).listTokens},
	{"POST /api/v1/tokens", (*server).createToken},
	{"DELETE /api/v1/tokens/{id}", (*server).deleteToken},
	{"GET /api/v1/audit", (*server).listAudit},
	{"POST /api/v1/backup", (*server).createBackup},
	{"GET /api/v1/users", (*server).listUsers},
	{"POST /api/v1/users", (*server).createUser},
	{"PATCH /api/v1/users/{id}", (*server).changeUser},
	{"DELETE /api/v1/users/{id}", (*server).deleteUser},
	{"GET /api/v1/users/{id}/environments", (*server).getUserEnvironments},
	{"PUT /api/v1/users/{id}/environments", (*server).setUserEnvironments},
	{"GET /api/v1/registries", (*server).listRegistries},
	{"POST /api/v1/registries", (*server).createRegistry},
	{"GET /api/v1/registries/{id}", (*server).getRegistry},
	{"PUT /api/v1/registries/{id}", (*server).updateRegistry},
	{"DELETE /api/v1/registries/{id}", (*server).deleteRegistry},
	{"GET /api/v1/registries/{id}/repositories", (*server).listRegistryRepositories},
	{"GET /api/v1/registries/{id}/tags", (*server).listRegistryTags},
	{"DELETE /api/v1/registries/{id}/tags", (*server).deleteRegistryTag},
	{"GET /api/v1/registries/{id}/manifest", (*server).getRegistryManifest},
	{"GET /api/v1/webhooks", (*server).listWebhooks},
	{"POST /api/v1/webhooks", (*server).createWebhook},
	{"GET /api/v1/webhooks/{id}", (*server).getWebhook},
	{"PUT /api/v1/webhooks/{id}", (*server).updateWebhook},
	{"DELETE /api/v1/webhooks/{id}", (*server).deleteWebhook},
	{"POST /api/v1/webhooks/{id}/test", (*server).testWebhook},
	{"GET /api/v1/environments", (*server).listEnvironments},
	{"POST /api/v1/environments", (*server).createEnvironment},
	{"GET /api/v1/environments/{env}", (*server).getEnvironment},
	{"DELETE /api/v1/environments/{env}", (*server).deleteEnvironment},
	{"POST /api/v1/environments/{env}/join-tokens", (*server).createJoinToken},
	{"DELETE /api/v1/environments/{env}/credentials", (*server).revokeAgent},
	{"POST /api/v1/agents/enroll", (*server).enrollAgent},
	{"GET /api/v1/agents/connect", (*server).connectAgent},
	{"GET /api/v1/environments/{env}/containers", (*server).listContainers},
	{"POST /api/v1/environments/{env}/containers", (*server).createContainer},
	{"GET /api/v1/environments/{env}/containers/{id}", (*server).getContainer},
	{"GET /api/v1/environments/{env}/containers/{id}/spec", (*server).getContainerSpec},
	{"POST /api/v1/environments/{env}/containers/{id}/recreate", (*server).recreateContainer},
	{"POST /api/v1/environments/{env}/user-containers", (*server).createTenantContainer},
	{"GET /api/v1/environments/{env}/user-containers/{id}/spec", (*server).getTenantSpec},
	{"POST /api/v1/environments/{env}/user-containers/{id}/recreate", (*server).recreateTenantContainer},
	{"POST /api/v1/environments/{env}/containers/{id}/update", (*server).updateContainer},
	{"DELETE /api/v1/environments/{env}/containers/{id}", (*server).removeContainer},
	{"POST /api/v1/environments/{env}/containers/{id}/start", (*server).startContainer},
	{"POST /api/v1/environments/{env}/containers/{id}/stop", (*server).stopContainer},
	{"POST /api/v1/environments/{env}/containers/{id}/restart", (*server).restartContainer},
	{"POST /api/v1/environments/{env}/containers/{id}/kill", (*server).killContainer},
	{"GET /api/v1/environments/{env}/containers/{id}/logs", (*server).containerLogs},
	{"GET /api/v1/environments/{env}/containers/{id}/stats", (*server).containerStats},
	{"GET /api/v1/environments/{env}/containers/{id}/exec", (*server).containerExec},
	{"GET /api/v1/environments/{env}/stats", (*server).environmentStats},
	{"GET /api/v1/environments/{env}/stats/summary", (*server).environmentSummary},
	{"GET /api/v1/environments/{env}/allowance", (*server).getAllowance},
	{"PUT /api/v1/environments/{env}/allowance", (*server).setAllowance},
	{"DELETE /api/v1/environments/{env}/allowance", (*server).deleteAllowance},
	{"GET /api/v1/environments/{env}/events", (*server).environmentEvents},
	{"GET /api/v1/environments/{env}/images", (*server).listImages},
	{"POST /api/v1/environments/{env}/images/pull", (*server).pullImage},
	{"POST /api/v1/environments/{env}/images/prune", (*server).pruneImages},
	{"GET /api/v1/environments/{env}/images/{id}", (*server).getImage},
	{"DELETE /api/v1/environments/{env}/images/{id}", (*server).removeImage},
	{"GET /api/v1/environments/{env}/volumes", (*server).listVolumes},
	{"POST /api/v1/environments/{env}/volumes", (*server).createVolume},
	{"POST /api/v1/environments/{env}/volumes/prune", (*server).pruneVolumes},
	{"POST /api/v1/environments/{env}/volumes/usage", (*server).measureVolumes},
	{"GET /api/v1/environments/{env}/volumes/{id}", (*server).getVolume},
	{"DELETE /api/v1/environments/{env}/volumes/{id}", (*server).removeVolume},
	{"GET /api/v1/environments/{env}/networks", (*server).listNetworks},
	{"POST /api/v1/environments/{env}/networks", (*server).createNetwork},
	{"POST /api/v1/environments/{env}/networks/prune", (*server).pruneNetworks},
	{"GET /api/v1/environments/{env}/networks/{id}", (*server).getNetwork},
	{"DELETE /api/v1/environments/{env}/networks/{id}", (*server).removeNetwork},
	{"POST /api/v1/stacks/check", (*server).checkStack},
	{"GET /api/v1/environments/{env}/stacks", (*server).listStacks},
	{"POST /api/v1/environments/{env}/stacks", (*server).createStack},
	{"GET /api/v1/environments/{env}/stacks/{id}", (*server).getStack},
	{"PUT /api/v1/environments/{env}/stacks/{id}", (*server).updateStack},
	{"DELETE /api/v1/environments/{env}/stacks/{id}", (*server).removeStack},
	{"POST /api/v1/environments/{env}/stacks/{id}/up", (*server).upStack},
	{"POST /api/v1/environments/{env}/stacks/{id}/down", (*server).downStack},
	{"POST /api/v1/environments/{env}/stacks/{id}/update", (*server).updateStackImages},
	{"GET /api/v1/system/update", (*server).getSystemUpdate},
	{"POST /api/v1/system/update/check", (*server).checkSystemUpdate},
	{"POST /api/v1/system/update", (*server).startSystemUpdate},
	{"GET /api/v1/environments/{env}/agent/update", (*server).getAgentUpdate},
	{"POST /api/v1/environments/{env}/agent/update/check", (*server).checkAgentUpdate},
	{"POST /api/v1/environments/{env}/agent/update", (*server).startAgentUpdate},
	{"GET /api/v1/environments/{env}/updates", (*server).listUpdates},
	{"POST /api/v1/environments/{env}/updates/check", (*server).checkUpdates},
	{"GET /api/v1/environments/{env}/updates/history", (*server).listUpdateHistory},
	{"GET /api/v1/environments/{env}/webhooks", (*server).listDeployHooks},
	{"POST /api/v1/environments/{env}/webhooks", (*server).createDeployHook},
	{"DELETE /api/v1/environments/{env}/webhooks/{id}", (*server).deleteDeployHook},
	{"POST /api/v1/environments/{env}/webhooks/{id}/secret", (*server).rotateDeployHookSecret},
	{"GET /api/v1/environments/{env}/webhooks/{id}/runs", (*server).listDeployHookRuns},
	{"POST /api/v1/hooks/{id}", (*server).triggerDeployHook},
	{"GET /api/v1/deployments", (*server).listDeployments},
	{"POST /api/v1/deployments", (*server).createDeployment},
	{"GET /api/v1/deployments/{name}", (*server).getDeployment},
	{"PATCH /api/v1/deployments/{name}", (*server).changeDeployment},
	{"DELETE /api/v1/deployments/{name}", (*server).deleteDeployment},
	{"GET /api/v1/deployments/{name}/members", (*server).listDeploymentMembers},
	{"POST /api/v1/deployments/{name}/members", (*server).setDeploymentMember},
	{"DELETE /api/v1/deployments/{name}/members/{user}", (*server).removeDeploymentMember},
	{"GET /api/v1/deployments/{name}/tokens", (*server).listDeployTokens},
	{"POST /api/v1/deployments/{name}/tokens", (*server).createDeployToken},
	{"DELETE /api/v1/deployments/{name}/tokens/{id}", (*server).deleteDeployToken},
	{"POST /api/v1/deployments/{name}/deploys", (*server).startDeploymentRun},
	{"GET /api/v1/deployments/{name}/deploys", (*server).listDeploymentRuns},
	{"GET /api/v1/deployments/{name}/deploys/{run}", (*server).getDeploymentRun},
	{"/api/", (*server).notFound},
}

// New returns the server's root handler: the API plus the web UI.
func New(opts Options) http.Handler {
	s := &server{
		log: opts.Log, version: opts.Version, auth: opts.Auth, envs: opts.Envs, registries: opts.Registries, audit: opts.Audit, updates: opts.Updates,
		webhooks: opts.Webhooks, events: opts.Events, deployHooks: opts.DeployHooks, deployments: opts.Deployments, usage: opts.Usage, backups: opts.Backups, allowances: opts.Allowances, tenant: opts.Tenant, deniedLabels: opts.DeniedLabels, registryClient: opts.RegistryClient,
		publicURL: opts.PublicURL, secureCookies: opts.SecureCookies, oidc: opts.OIDC, oidcName: opts.OIDCName, noPasswordLogin: opts.NoPasswordLogin, recreating: map[string]bool{},
		stacksDir: opts.StacksDir, stackRunner: opts.StackRunner, stackChecker: opts.StackChecker, stackBusy: map[string]bool{}, summaries: map[summaryKey]*summaryEntry{},
		self: opts.Self, dockerHost: opts.DockerHost, agentSelf: opts.AgentSelf,
		checks:         make(chan struct{}, maxStackChecks),
		done:           context.Background(),
		userRegistries: newRegistrySet(opts.UserRegistries),
	}
	if s.events == nil {
		s.events = events.New(events.Options{Context: opts.Context, Log: opts.Log})
	}
	if s.registryClient == nil {
		s.registryClient = registryclient.New(registryclient.Options{})
	}
	if opts.Context != nil {
		s.done = opts.Context
	}
	s.after = opts.After
	if s.after == nil {
		s.after = time.After
	}
	s.now = opts.Now
	if s.now == nil {
		s.now = time.Now
	}
	if s.stackRunner == nil {
		s.stackRunner = func(environments.Environment) stacks.Runner { return nil }
	}
	api := http.NewServeMux()
	for _, rt := range routes {
		api.HandleFunc(rt.pattern, func(w http.ResponseWriter, r *http.Request) { rt.handler(s, w, r) })
	}

	// tokenRefused answers the session-only routes for token requests, through a mux so
	// the audit entry gets the route and its path values.
	s.tokenRefused = http.NewServeMux()
	for pattern := range sessionOnlyRoutes {
		s.tokenRefused.HandleFunc(pattern, s.refuseToken)
	}

	// adminOnly answers users on every other route, through a mux for the audit entry too.
	s.adminOnly = http.NewServeMux()
	for _, rt := range routes {
		if !userRoutes[rt.pattern] && !publicRoutes[rt.pattern] {
			s.adminOnly.HandleFunc(rt.pattern, s.refuseUser)
		}
	}

	s.deployTokens = s.deployTokenRoutes()

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

	// self, dockerHost and agentSelf find nudge's own containers (Options).
	self       stacks.Self
	dockerHost string
	agentSelf  func(ctx context.Context, env environments.Environment) (stacks.Self, string, error)

	registries     *registries.Service
	registryClient RegistryBrowser
	audit          *audit.Service
	updates        *updates.Service
	webhooks       *notify.Service
	// deployHooks are the deploy webhooks.
	deployHooks *deployhooks.Service
	deployments *deployments.Service
	// deployTokens serves the requests that carry a deploy token.
	deployTokens *http.ServeMux
	usage        *usage.Service
	backups      *backup.Service
	allowances   *allowances.Service
	// tenant is the host-side hardening of users' containers (tenant_host.go).
	tenant TenantHosting
	// userRegistries are the registries users' pulls may reach (pull_registry.go).
	userRegistries registrySet
	// deniedLabels is Options.DeniedLabels.
	deniedLabels []string
	// done ends at shutdown (Options.Context).
	done context.Context
	// events fans each environment's Docker events out to the clients watching it.
	events *events.Hub

	publicURL     string
	secureCookies bool
	// oidc is the SSO client, nil when SSO is off.
	oidc            *oidc.Client
	oidcName        string
	noPasswordLogin bool

	stacksDir    string
	stackRunner  func(env environments.Environment) stacks.Runner
	stackChecker func(ctx context.Context, f stacks.Files) ([]string, error)
	// checks holds a slot per running stack check.
	checks chan struct{}
	// tokenRefused serves sessionOnlyRoutes to API tokens.
	tokenRefused *http.ServeMux
	// adminOnly serves the routes outside userRoutes to users.
	adminOnly *http.ServeMux

	mu sync.Mutex
	// recreating holds "<env>/<container ID>" of the recreates running now.
	recreating map[string]bool
	// stackBusy holds stackKey of the stacks being changed or deployed now.
	stackBusy map[string]bool
	// summaries holds each environment's last usage totals, by environment and owner.
	summaries map[summaryKey]*summaryEntry
	// after is Options.After.
	after func(time.Duration) <-chan time.Time
	// now is Options.Now.
	now func() time.Time
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
		if isDeployToken(r) {
			// Its own routes, which check the token themselves; it reaches nothing else.
			s.serveAudited(s.deployTokens, w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			withToken.ServeHTTP(w, r)
			return
		}
		withSession.ServeHTTP(w, r)
	})
}

// authenticate serves public routes as they are and every other route only to the user
// that lookup finds, and to a user (not an admin) only userRoutes. With token set (lookup
// checks an API token, which has its user's role) sessionOnlyRoutes are refused.
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
		if !userRoutes[pattern] && !isAdmin(u) {
			s.serveAudited(s.adminOnly, w, r)
			return
		}
		s.serveAudited(api, w, r)
	})
}

func (s *server) refuseToken(w http.ResponseWriter, _ *http.Request) {
	s.writeError(w, http.StatusForbidden, "API tokens can't do this; sign in to the web UI")
}

func (s *server) refuseUser(w http.ResponseWriter, _ *http.Request) {
	s.writeError(w, http.StatusForbidden, "admins only")
}

// isAdmin reports whether u may do admin-only actions.
func isAdmin(u auth.User) bool { return u.Role == auth.RoleAdmin }

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
