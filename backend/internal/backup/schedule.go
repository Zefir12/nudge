package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Dir is the directory below the data directory that scheduled backups go to.
const Dir = "backups"

// Save writes a backup to <data>/backups/ and removes the oldest ones beyond keep. It
// returns the new backup's path.
func (s *Service) Save(ctx context.Context, passphrase string, keep int) (string, error) {
	if len([]rune(passphrase)) < MinPassphrase {
		return "", ErrPassphrase
	}
	if !s.mu.TryLock() {
		return "", ErrBusy
	}
	defer s.mu.Unlock()
	dir := filepath.Join(s.dataDir, Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".tmp-*"+Ext)
	if err != nil {
		return "", fmt.Errorf("create backup: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = s.write(ctx, f, passphrase)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("create backup: %w", cerr)
	}
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, FileName(s.now()))
	if err := os.Rename(f.Name(), dst); err != nil {
		return "", fmt.Errorf("create backup: %w", err)
	}
	if err := prune(dir, keep); err != nil {
		return dst, err
	}
	return dst, nil
}

// prune removes the oldest backups in dir beyond keep; names sort by time.
func prune(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("prune backups: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), "nudge-") && strings.HasSuffix(e.Name(), Ext) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	for len(names) > keep {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return fmt.Errorf("prune backups: %w", err)
		}
		names = names[1:]
	}
	return nil
}

// Run saves a backup every interval until ctx ends, keeping the newest keep.
func (s *Service) Run(ctx context.Context, interval time.Duration, passphrase string, keep int) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			path, err := s.Save(ctx, passphrase, keep)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Error("scheduled backup", "err", err)
				}
				continue
			}
			s.log.Info("saved backup", "path", path)
		}
	}
}
