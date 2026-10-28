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

	"git.zefirlabs.net/zefir/nudge/backend/internal/allowances"
	"git.zefirlabs.net/zefir/nudge/backend/internal/api"
	"git.zefirlabs.net/zefir/nudge/backend/internal/audit"
	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/backup"
	"git.zefirlabs.net/zefir/nudge/backend/internal/certs"
	"git.zefirlabs.net/zefir/nudge/backend/internal/cli"
	"git.zefirlabs.net/zefir/nudge/backend/internal/config"
	"git.zefirlabs.net/zefir/nudge/backend/internal/deployhooks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/deployments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/events"
	"git.zefirlabs.net/zefir/nudge/backend/internal/notify"
	"git.zefirlabs.net/zefir/nudge/backend/internal/oidc"
	"git.zefirlabs.net/zefir/nudge/backend/internal/probe"
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
  deploy   start a deployment run and follow it (for CI; token in NUDGE_DEPLOY_TOKEN)
  probe    one HTTP check (what a deployment's HTTP check runs in a helper container)
  restore  restore a backup into the data directory (with the server stopped)
  version  print the version

Run "nudge <command> -h" for a command's flags.
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
		return cli.Agent(ctx, args, getenv, stderr, "")
	case "deploy":
		return runDeploy(ctx, args, getenv, stdout, stderr)
	case "probe":
		return cli.Probe(ctx, args, stdout, stderr)
	case "restore":
		return runRestore(ctx, args, getenv, stdout, stderr)
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
	if err := cli.PrepareDataDir(cfg.DataDir); err != nil {
		return err
	}
	if err := cli.CleanComposeConfigs(cli.ComposeConfigDir(cfg.DataDir), log); err != nil {
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
	backups := backup.New(backup.Options{Store: st, DataDir: cfg.DataDir, StacksDir: cfg.StacksDir, Version: version.Version, Log: log})
	var backupPassphrase string
	if cfg.Backup.Interval > 0 {
		if backupPassphrase, err = backup.ReadPassphrase(cfg.Backup.PassphraseFile); err != nil {
			return err
		}
	}
	regs := registries.New(registries.Options{DB: st.DB(), Box: box})

	authSvc := auth.New(auth.Options{
		DB: st.DB(), Log: log, SSOSessionTTL: cfg.OIDC.SessionTTL, AdminPasswordOnly: cfg.NoPasswordLogin,
	})
	var sso *oidc.Client
	if cfg.OIDC.Enabled() {
		sso = oidc.New(oidc.Options{
			Issuer: cfg.OIDC.Issuer, ClientID: cfg.OIDC.ClientID, ClientSecret: cfg.OIDC.ClientSecret,
			RedirectURL: cfg.PublicURL + "/api/v1/auth/oidc/callback",
			AdminRole:   cfg.OIDC.AdminRole, UserRole: cfg.OIDC.UserRole, RequireMFA: cfg.OIDC.RequireMFA, Log: log,
		})
		log.Info("single sign-on on", "issuer", cfg.OIDC.Issuer, "password_login", !cfg.NoPasswordLogin)
	}
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
	if cfg.Backup.Interval > 0 {
		wg.Go(func() { backups.Run(cleanupCtx, cfg.Backup.Interval, backupPassphrase, cfg.Backup.Keep) })
		log.Info("scheduled backups on", "interval", cfg.Backup.Interval, "keep", cfg.Backup.Keep, "dir", filepath.Join(cfg.DataDir, backup.Dir))
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

			Registries:   regs,
			Audit:        auditSvc,
			Updates:      upd,
			Webhooks:     hooks,
			DeployHooks:  deployHooks,
			Deployments:  deps,
			Usage:        usageSvc,
			Backups:      backups,
			Allowances:   allowances.New(allowances.Options{DB: st.DB()}),
			Tenant:       api.TenantHosting{CgroupParent: cfg.Tenant.CgroupParent, Runtime: cfg.Tenant.Runtime, StorageSize: cfg.Tenant.StorageSize},
			DeniedLabels: cfg.Tenant.DeniedLabels,
			// Users' pulls are anonymous, but a reference names any host the daemon can
			// reach: only these registries.
			UserRegistries: cfg.UserRegistries,
			Events:         hub,
			Web:            web.Handler(web.Dist()),

			StacksDir:   cfg.StacksDir,
			StackRunner: stackRunner(cfg, envs, regs, log),
			// Drafts are checked next to the stacks, on the data volume.
			StackChecker: stacks.NewChecker(stacks.CheckerOptions{TempDir: cfg.StacksDir, Compose: cfg.Compose}).Check,

			PublicURL:      cfg.PublicURL,
			SecureCookies:  cfg.TLS() || strings.HasPrefix(cfg.PublicURL, "https://"),
			TrustedProxies: cfg.TrustedProxies,

			OIDC:            sso,
			OIDCName:        cfg.OIDC.Name,
			NoPasswordLogin: cfg.NoPasswordLogin,
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
		prober := probe.New(probe.Options{
			Docker: func() (probe.Docker, error) { return envs.Client(env) },
			Self:   self.ID, Hostname: self.Hostname,
		})
		return stacks.NewLocal(stacks.LocalOptions{
			Dir: dir, Compose: cfg.Compose, DockerHost: cfg.DockerHost, Credentials: creds,
			ConfigDir: cli.ComposeConfigDir(cfg.DataDir),
			Mounts:    stacks.ContainerMounts(self, func() (stacks.Inspector, error) { return envs.Client(env) }),
			Log:       log.With("env", env.ID),
			Probe:     prober.Run,
		})
	}
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
