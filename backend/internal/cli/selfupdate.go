package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/selfupdate"
)

// selfUpdateTimeout bounds a whole swap, the new container's settling included.
const selfUpdateTimeout = 10 * time.Minute

// SelfUpdate is `self-update [-docker-host H] CONTAINER`, what the helper container a
// self-update starts runs: it replaces CONTAINER (the server's or an agent's) with one of
// the image its reference names now, and puts the old one back if that fails.
func SelfUpdate(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("nudge self-update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("docker-host", docker.DefaultHost, "Docker API, unix://")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: nudge self-update [-docker-host H] CONTAINER")
		return 2
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	dial, err := docker.LocalDialer(*host)
	if err != nil {
		log.Error("self-update", "err", err)
		return 2
	}
	dc, err := docker.New(dial)
	if err != nil {
		log.Error("self-update", "err", err)
		return 1
	}
	defer func() { _ = dc.Close() }()
	ctx, cancel := context.WithTimeout(ctx, selfUpdateTimeout)
	defer cancel()
	err = selfupdate.Swap(ctx, dc, fs.Arg(0), selfupdate.SwapOptions{Log: log})
	switch {
	case errors.Is(err, selfupdate.ErrCurrent):
		log.Info("self-update: nothing to do", "reason", err.Error())
		return 0
	case err != nil:
		log.Error("self-update failed", "err", err)
		return 1
	}
	return 0
}
