// Command nudge is a self-hosted Docker manager: `nudge serve` runs the API and web UI.
// `nudge agent`, which connects a remote Docker host to a server, comes with PLAN.md
// phase 2.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/api"
	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/config"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
	"git.zefirlabs.net/zefir/nudge/backend/internal/version"
	"git.zefirlabs.net/zefir/nudge/backend/internal/web"
)

const usage = `usage: nudge [command] [flags]

commands:
  serve    run the API and web UI (default)
  version  print the version

Run "nudge serve -h" for its flags.
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

func serve(ctx context.Context, cfg config.Server, log *slog.Logger) (err error) {
	if err := prepareDataDir(cfg.DataDir); err != nil {
		return err
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

	authSvc := auth.New(auth.Options{DB: st.DB(), Log: log})
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

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: api.New(api.Options{
			Log:     log,
			Version: version.Version,
			Auth:    authSvc,
			Envs:    envs,
			Web:     web.Handler(web.Dist()),
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "version", version.Version)
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
