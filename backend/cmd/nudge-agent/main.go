// Command nudge-agent connects a remote Docker host to a nudge server. It is the agent
// image's binary: only the agent, its stacks runner and the HTTP check helper, none of
// the server's API, database or web UI.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"git.zefirlabs.net/zefir/nudge/backend/internal/cli"
	"git.zefirlabs.net/zefir/nudge/backend/internal/version"
)

const usage = `usage: nudge-agent [command] [flags]

commands:
  agent        connect this host's Docker to a nudge server (default)
  probe        one HTTP check (what a deployment's HTTP check runs in a helper container)
  self-update  replace this container with its image's newer version (what a self-update's helper runs)
  version      print the version

Run "nudge-agent <command> -h" for a command's flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := realMain(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func realMain(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cmd := "agent"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "agent":
		return cli.Agent(ctx, args, getenv, stderr, "")
	case "probe":
		return cli.Probe(ctx, args, stdout, stderr)
	case "self-update":
		return cli.SelfUpdate(ctx, args, stderr)
	case "version":
		_, _ = fmt.Fprintln(stdout, version.Version)
		return 0
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "nudge-agent: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}
