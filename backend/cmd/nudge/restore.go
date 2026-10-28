package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"git.zefirlabs.net/zefir/nudge/backend/internal/backup"
	"git.zefirlabs.net/zefir/nudge/backend/internal/config"
)

// runRestore is `nudge restore`: it unpacks a backup into the data directory of a
// stopped server.
func runRestore(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cfg, err := config.LoadRestore(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "nudge restore:", err)
		return 2
	}
	if err := restore(ctx, cfg, stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, "nudge restore:", err)
		if errors.Is(err, backup.ErrNotEmpty) {
			_, _ = fmt.Fprintln(stderr, "Stop the server first. -force moves the data that is there aside and restores the backup.")
		}
		return 1
	}
	return 0
}

func restore(ctx context.Context, cfg config.Restore, stdout io.Writer) error {
	pass, err := backup.ReadPassphrase(cfg.PassphraseFile)
	if err != nil {
		return err
	}
	f, err := os.Open(cfg.File)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer func() { _ = f.Close() }()
	m, aside, err := backup.Restore(ctx, f, backup.RestoreOptions{
		DataDir: cfg.DataDir, StacksDir: cfg.StacksDir, Passphrase: pass, Force: cfg.Force,
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Restored the backup of %s (nudge %s, %d stacks) into %s.\n",
		m.Created.Format("2006-01-02 15:04:05 MST"), m.Version, m.Stacks, cfg.DataDir)
	if aside != "" {
		_, _ = fmt.Fprintf(stdout, "The data that was there is in %s, its stacks next to %s.\n", aside, cfg.StacksDir)
	}
	return nil
}
