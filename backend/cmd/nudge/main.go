// Command nudge is a self-hosted Docker manager: `nudge serve` runs the API and web UI,
// `nudge agent` connects a remote Docker host to a server.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/agent"
	"git.zefirlabs.net/zefir/nudge/backend/internal/api"
	"git.zefirlabs.net/zefir/nudge/backend/internal/audit"
	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/certs"
	"git.zefirlabs.net/zefir/nudge/backend/internal/config"
	"git.zefirlabs.net/zefir/nudge/backend/internal/deployhooks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/deployments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/events"
	"git.zefirlabs.net/zefir/nudge/backend/internal/notify"
	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
	"git.zefirlabs.net/zefir/nudge/backend/internal/updates"
	diskusage "git.zefirlabs.net/zefir/nudge/backend/internal/usage"
	"git.zefirlabs.net/zefir/nudge/backend/internal/version"
	"git.zefirlabs.net/zefir/nudge/backend/internal/web"
)

const usage = `usage: nudge [command] [flags]

commands:
  serve    run the API and web UI (default)
  agent    connect this host's Docker to a nudge server
  version  print the version

Run "nudge serve -h" or "nudge agent -h" for their flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := realMain(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func realMain(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		cfg, err := config.LoadServer(args, getenv, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "nudge:", err)
			return 2
		}
		log := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
		if err := serve(ctx, cfg, log); err != nil {
			log.Error("server stopped", "err", err)
			return 1
		}
		return 0
	case "agent":
		cfg, err := config.LoadAgent(args, getenv, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "nudge:", err)
			return 2
		}
		log := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
		if err := runAgent(ctx, cfg, log); err != nil {
			log.Error("agent stopped", "err", err)
			return 1
		}
		return 0
	case "version":
		_, _ = fmt.Fprintln(stdout, version.Version)
		return 0
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "nudge: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

// warnTrustedProxies warns about a trusted proxy prefix that covers every address: then
// anyone can forge X-Forwarded-For and X-Forwarded-Proto.
func warnTrustedProxies(log *slog.Logger, proxies []netip.Prefix) {
	for _, p := range proxies {
		if p.Bits() == 0 {
			log.Warn("trusted proxies include every address: any client can forge X-Forwarded-For and X-Forwarded-Proto", "prefix", p.String())
		}
	}
}

func serve(ctx context.Context, cfg config.Server, log *slog.Logger) (err error) {
	warnTrustedProxies(log, cfg.TrustedProxies)
	if err := prepareDataDir(cfg.DataDir); err != nil {
		return err
	}
	if err := cleanComposeConfigs(composeConfigDir(cfg.DataDir), log); err != nil {
		return err
	}
	var certLoader *certs.Loader
	if cfg.TLS() {
		if certLoader, err = certs.Load(cfg.TLSCert, cfg.TLSKey, log); err != nil {
			return err
		}
	}
	// Not cancelled by a signal: migrations are short, and a signal during startup should
	// still end in a clean shutdown below rather than a failed start. The timeout bounds
	// a database locked by another process.
	openCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	st, err := store.Open(openCtx, cfg.DataDir)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	box, err := secretbox.LoadKey(cfg.DataDir)
	if err != nil {
		return err
	}
	regs := registries.New(registries.Options{DB: st.DB(), Box: box})

	authSvc := auth.New(auth.Options{DB: st.DB(), Log: log})
	auditSvc := audit.New(audit.Options{DB: st.DB(), Log: log, Retention: time.Duration(cfg.AuditRetentionDays) * 24 * time.Hour})
	if cfg.AdminPassword != "" {
		// Like the migrations: part of startup, not cancelled by a signal.
		adminCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		created, err := authSvc.EnsureAdmin(adminCtx, cfg.AdminPassword)
		cancel()
		if err != nil {
			return fmt.Errorf("NUDGE_ADMIN_PASSWORD: %w", err)
		}
		if created {
			log.Info("created admin user from NUDGE_ADMIN_PASSWORD", "username", auth.AdminUsername)
		}
	}
	connect, err := environments.LocalConnect(cfg.DockerHost)
	if err != nil {
		return err
	}
	envs := environments.New(environments.Options{DB: st.DB(), Log: log, Connect: connect})
	defer func() {
		if cerr := envs.Close(); cerr != nil {
			log.Warn("closing Docker clients", "err", cerr)
		}
	}()
	// Part of startup like the admin above; EnsureLocal bounds its own ping.
	created, err := envs.EnsureLocal(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	if created {
		log.Info("created local environment", "docker_host", cfg.DockerHost)
	}

	// The cleanup stops before the deferred st.Close above.
	var wg sync.WaitGroup
	cleanupCtx, stopCleanup := context.WithCancel(ctx)
	defer func() {
		stopCleanup()
		wg.Wait()
	}()
	wg.Go(func() { authSvc.RunCleanup(cleanupCtx, 10*time.Minute) })
	wg.Go(func() { auditSvc.RunCleanup(cleanupCtx, time.Hour) })
	hooks := notify.New(notify.Options{DB: st.DB(), Box: box, Log: log, Version: version.Version})
	wg.Go(func() { hooks.Run(cleanupCtx) })
	// One Docker event stream per environment serves the UI and the notifications.
	hub := events.New(events.Options{Context: ctx, Log: log})
	watcher := notify.NewWatcher(notify.WatchOptions{Envs: envs, Hub: hub, Send: hooks, Log: log})
	wg.Go(func() { watcher.Run(cleanupCtx) })
	// The runs end with ctx, so ?wait=1 triggers don't hold up the shutdown; Close waits
	// for them before the database closes.
	deployHooks := deployhooks.New(deployhooks.Options{Context: ctx, DB: st.DB(), Box: box, Log: log})
	defer deployHooks.Close()
	if err := deployHooks.FailStale(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	deps := deployments.New(deployments.Options{Context: ctx, DB: st.DB(), Log: log})
	defer deps.Close()
	if err := deps.FailStale(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	upd := updates.New(updates.Options{DB: st.DB(), Log: log, Envs: envs, Auth: regs.AuthFor, Checked: hooks.UpdatesChecked})
	if cfg.UpdateCheckInterval > 0 {
		// The first check waits for agents to reconnect after a restart.
		wg.Go(func() { upd.Run(cleanupCtx, 2*time.Minute, cfg.UpdateCheckInterval) })
	}
	usageSvc := diskusage.New(diskusage.Options{DB: st.DB(), Log: log, Envs: envs})
	if cfg.UsageInterval > 0 {
		// Like the update check, the first measurement waits for agents to reconnect.
		wg.Go(func() { usageSvc.Run(cleanupCtx, 2*time.Minute, cfg.UsageInterval) })
	}
	if certLoader != nil {
		// SIGHUP would otherwise kill the process.
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		wg.Go(func() { certLoader.Watch(cleanupCtx, ticker.C, hup) })
	}

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: api.New(api.Options{
			Context: ctx,
			Log:     log,
			Version: version.Version,
			Auth:    authSvc,
			Envs:    envs,

			Registries:  regs,
			Audit:       auditSvc,
			Updates:     upd,
			Webhooks:    hooks,
			DeployHooks: deployHooks,
			Deployments: deps,
			Usage:       usageSvc,
			Events:      hub,
			Web:         web.Handler(web.Dist()),

			StacksDir:   cfg.StacksDir,
			StackRunner: stackRunner(cfg, envs, regs, log),
			// Drafts are checked next to the stacks, on the data volume.
			StackChecker: stacks.NewChecker(stacks.CheckerOptions{TempDir: cfg.StacksDir, Compose: cfg.Compose}).Check,

			PublicURL:      cfg.PublicURL,
			SecureCookies:  cfg.TLS() || strings.HasPrefix(cfg.PublicURL, "https://"),
			TrustedProxies: cfg.TrustedProxies,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	if certLoader != nil {
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certLoader.GetCertificate}
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "tls", cfg.TLS(), "public_url", cfg.PublicURL, "trusted_proxies", cfg.TrustedProxies, "version", version.Version)
		if certLoader != nil {
			errc <- srv.ListenAndServeTLS("", "")
			return
		}
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	// ListenAndServe returns ErrServerClosed once Shutdown started; wait for it so the
	// goroutine doesn't outlive serve.
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen: %w", err)
	}
	return nil
}

func runAgent(ctx context.Context, cfg config.Agent, log *slog.Logger) error {
	if err := prepareDataDir(cfg.DataDir); err != nil {
		return err
	}
	if err := cleanComposeConfigs(composeConfigDir(cfg.DataDir), log); err != nil {
		return err
	}
	dial, err := docker.LocalDialer(cfg.DockerHost)
	if err != nil {
		return err
	}
	if cfg.ServerURL.Scheme == "http" {
		log.Warn("connecting over http:// (-insecure): the agent secret and all Docker traffic travel in clear text")
	}
	hostname, err := os.Hostname()
	if err != nil {
		log.Warn("hostname unknown", "err", err)
	}
	log.Info("agent starting", "server", cfg.ServerURL.Redacted(), "docker_host", cfg.DockerHost, "version", version.Version)
	err = agent.New(agent.Options{
		ServerURL: cfg.ServerURL,
		JoinToken: cfg.JoinToken,
		DataDir:   cfg.DataDir,
		Dial:      dial,
		Control: stacks.NewControl(stacks.NewLocal(stacks.LocalOptions{
			Dir: stacks.NewDir(cfg.StacksDir), Compose: cfg.Compose, DockerHost: cfg.DockerHost,
			ConfigDir: composeConfigDir(cfg.DataDir),
		}), log),
		Version:  version.Version,
		Hostname: hostname,
		Log:      log,
	}).Run(ctx)
	if err == nil {
		log.Info("agent stopped")
	}
	return err
}

// stackRunner deploys the local environment's stacks with compose here, and an agent
// environment's through its agent's control service; both pull with the stored registry
// credentials.
func stackRunner(cfg config.Server, envs *environments.Service, regs *registries.Service, log *slog.Logger) func(environments.Environment) stacks.Runner {
	creds := stackCredentials(regs)
	// Up checks a local stack's binds against the mounts of nudge's own container.
	self := stacks.SelfID(os.ReadFile, os.Hostname)
	return func(env environments.Environment) stacks.Runner {
		// The server's copy of the environment's stacks (api's stackDir).
		dir := stacks.NewDir(filepath.Join(cfg.StacksDir, strconv.FormatInt(env.ID, 10)))
		if env.Kind == environments.KindAgent {
			return stacks.NewRemote(envs.ControlDialer(env.ID)).WithCredentials(dir, creds)
		}
		return stacks.NewLocal(stacks.LocalOptions{
			Dir: dir, Compose: cfg.Compose, DockerHost: cfg.DockerHost, Credentials: creds,
			ConfigDir: composeConfigDir(cfg.DataDir),
			Mounts:    stacks.ContainerMounts(self, func() (stacks.Inspector, error) { return envs.Client(env) }),
			Log:       log.With("env", env.ID),
		})
	}
}

// composeConfigDir is where compose runs get their temporary Docker configs with the
// registry credentials: on the data volume, private to nudge (not in the stacks
// directory, which stacks may bind-mount).
func composeConfigDir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "compose-config")
}

// cleanComposeConfigs prepares dir and removes the Docker configs a killed run left there.
func cleanComposeConfigs(dir string, log *slog.Logger) error {
	n, err := stacks.CleanConfigDir(dir)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Warn("removed docker configs left by an interrupted compose run", "count", n, "dir", dir)
	}
	return nil
}

// stackCredentials finds the stored credentials of the registries a stack's files name.
func stackCredentials(regs *registries.Service) stacks.CredentialFunc {
	return func(ctx context.Context, f stacks.Files) ([]stacks.Credential, error) {
		auths, err := regs.AuthsFor(ctx, f.Compose, f.Env)
		if err != nil {
			return nil, err
		}
		creds := make([]stacks.Credential, 0, len(auths))
		for _, a := range auths {
			creds = append(creds, stacks.Credential{Address: a.Address, Username: a.Username, Password: a.Password})
		}
		return creds, nil
	}
}

// prepareDataDir creates the data directory (0700) if it is missing and checks that it
// is writable, so a bad volume fails at start with a clear message instead of on the
// first write.
func prepareDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return fmt.Errorf("data directory %s is not writable: %w", dir, err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return fmt.Errorf("data directory %s is not writable: %w", dir, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("data directory %s: remove %s: %w", dir, filepath.Base(name), err)
	}
	return nil
}
