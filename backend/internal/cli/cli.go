// Package cli holds the subcommands both binaries run: `agent` and `probe` are
// nudge-agent's, and nudge keeps them for the server image (`probe` runs in the
// local environment's helper containers, `agent` for hosts not yet moved to the
// nudge-agent image). The data directory helpers are shared with `nudge serve`.
package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

// ComposeConfigDir is where compose runs get their temporary Docker configs with the
// registry credentials: on the data volume, private to nudge (not in the stacks
// directory, which stacks may bind-mount).
func ComposeConfigDir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "compose-config")
}

// CleanComposeConfigs prepares dir and removes the Docker configs a killed run left there.
func CleanComposeConfigs(dir string, log *slog.Logger) error {
	n, err := stacks.CleanConfigDir(dir)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Warn("removed docker configs left by an interrupted compose run", "count", n, "dir", dir)
	}
	return nil
}

// PrepareDataDir creates the data directory (0700) if it is missing and checks that it
// is writable, so a bad volume fails at start with a clear message instead of on the
// first write.
func PrepareDataDir(dir string) error {
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
