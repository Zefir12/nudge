package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/probe"
)

// Probe is `probe [-status N] [-timeout D] URL`, the HTTP check a deployment runs in a
// helper container started from the server's or the agent's own image: one GET, exit 0
// when the answer has the status.
func Probe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nudge probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	status := fs.Int("status", 200, "expected HTTP status")
	timeout := fs.Duration("timeout", 5*time.Second, "how long to wait for the answer")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return probe.ExitBadInput
	}
	if fs.NArg() != 1 || *timeout <= 0 || *timeout > probe.MaxTimeout {
		_, _ = fmt.Fprintln(stderr, "usage: nudge probe [-status N] [-timeout D] URL")
		return probe.ExitBadInput
	}
	code, msg := probe.Check(ctx, fs.Arg(0), *status, *timeout)
	_, _ = fmt.Fprintln(stdout, msg)
	return code
}
