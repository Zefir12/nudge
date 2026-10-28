package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"git.zefirlabs.net/zefir/nudge/backend/internal/agent"
	"git.zefirlabs.net/zefir/nudge/backend/internal/config"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/probe"
	"git.zefirlabs.net/zefir/nudge/backend/internal/selfupdate"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/version"
)

// Agent is the agent subcommand: parse args (NUDGE_* fallbacks through getenv) and
// connect this host's Docker to a server until ctx ends. A non-empty warning is logged
// once at start. It returns the process exit code.
func Agent(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer, warning string) int {
	cfg, err := config.LoadAgent(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "nudge:", err)
		return 2
	}
	log := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if warning != "" {
		log.Warn(warning)
	}
	if err := runAgent(ctx, cfg, log); err != nil {
		log.Error("agent stopped", "err", err)
		return 1
	}
	return 0
}

func runAgent(ctx context.Context, cfg config.Agent, log *slog.Logger) error {
	if err := PrepareDataDir(cfg.DataDir); err != nil {
		return err
	}
	if err := CleanComposeConfigs(ComposeConfigDir(cfg.DataDir), log); err != nil {
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
	// HTTP checks start a helper container from the agent's own image.
	dc, err := docker.New(dial)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()
	self := stacks.SelfID(os.ReadFile, os.Hostname)
	prober := probe.New(probe.Options{
		Docker: func() (probe.Docker, error) { return dc, nil },
		Self:   self.ID, Hostname: self.Hostname,
	})
	log.Info("agent starting", "server", cfg.ServerURL.Redacted(), "docker_host", cfg.DockerHost, "version", version.Version)
	err = agent.New(agent.Options{
		ServerURL: cfg.ServerURL,
		JoinToken: cfg.JoinToken,
		DataDir:   cfg.DataDir,
		Dial:      dial,
		// The server asks the control service for the agent's container to update it.
		Control: selfupdate.Control(self, cfg.DockerHost, stacks.NewControl(stacks.NewLocal(stacks.LocalOptions{
			Dir: stacks.NewDir(cfg.StacksDir), Compose: cfg.Compose, DockerHost: cfg.DockerHost,
			ConfigDir: ComposeConfigDir(cfg.DataDir), Probe: prober.Run,
		}), log)),
		Version:  version.Version,
		Hostname: hostname,
		Log:      log,
	}).Run(ctx)
	if err == nil {
		log.Info("agent stopped")
	}
	return err
}
