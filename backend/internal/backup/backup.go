// Package backup writes the server's data (a snapshot of the database, the secret key and
// the stacks' compose files) into one passphrase-encrypted archive, and restores one into
// a data directory. Backups can be made while the server runs; restores run offline.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

// Names inside an archive. Stacks are under stacks/<environment ID>/<name>/.
const (
	manifestName = "manifest.json"
	databaseName = "nudge.db"
	keyName      = "secret.key"
	stacksPrefix = "stacks/"
)

// Ext is a backup file's extension.
const Ext = ".nudgebak"

// MinPassphrase is the shortest passphrase a backup takes. The archive holds the key to
// the registry passwords, so it gets a passphrase that resists guessing offline.
const MinPassphrase = 12

// ErrBusy means another backup is being written.
var ErrBusy = errors.New("a backup is already running")

// ErrPassphrase means a passphrase shorter than MinPassphrase.
var ErrPassphrase = fmt.Errorf("the passphrase must have at least %d characters", MinPassphrase)

// Manifest is the archive's first entry.
type Manifest struct {
	Format  int       `json:"format"`
	Version string    `json:"version"`
	Schema  int       `json:"schema"`
	Created time.Time `json:"created"`
	Stacks  int       `json:"stacks"`
}

// Options configure a Service.
type Options struct {
	Store     *store.Store
	DataDir   string
	StacksDir string
	// Version is nudge's version, recorded in the manifest.
	Version string
	Log     *slog.Logger
	// Now is time.Now outside tests.
	Now func() time.Time
}

// Service writes backups, one at a time.
type Service struct {
	store     *store.Store
	dataDir   string
	stacksDir string
	version   string
	log       *slog.Logger
	now       func() time.Time
	kdf       kdf
	mu        sync.Mutex
}

// New returns a Service.
func New(o Options) *Service {
	s := &Service{store: o.Store, dataDir: o.DataDir, stacksDir: o.StacksDir, version: o.Version, log: o.Log, now: o.Now, kdf: defaultKDF}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s
}

// FileName is the name a backup made at t gets.
func FileName(t time.Time) string {
	return "nudge-" + t.UTC().Format("20060102-150405") + Ext
}

// Create writes a backup to an unnamed temporary file in the data directory and returns
// it open at its start, with its manifest; closing it frees the space. It fails with
// ErrBusy while another backup runs.
func (s *Service) Create(ctx context.Context, passphrase string) (*os.File, Manifest, error) {
	if len([]rune(passphrase)) < MinPassphrase {
		return nil, Manifest{}, ErrPassphrase
	}
	if !s.mu.TryLock() {
		return nil, Manifest{}, ErrBusy
	}
	defer s.mu.Unlock()
	f, err := os.CreateTemp(s.dataDir, ".backup-*"+Ext)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("create backup: %w", err)
	}
	// Unlinked at once: nothing is left behind if the process dies.
	if err := os.Remove(f.Name()); err != nil {
		_ = f.Close()
		return nil, Manifest{}, fmt.Errorf("create backup: %w", err)
	}
	m, err := s.write(ctx, f, passphrase)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = f.Close()
		return nil, Manifest{}, err
	}
	return f, m, nil
}

// write writes the archive to w: the manifest, the database snapshot, the key and the
// stacks' compose files, as tar, gzipped, encrypted.
func (s *Service) write(ctx context.Context, w io.Writer, passphrase string) (Manifest, error) {
	tmp, err := os.MkdirTemp(s.dataDir, ".backup-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create backup: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	db := filepath.Join(tmp, databaseName)
	if err := s.store.Snapshot(ctx, db); err != nil {
		return Manifest{}, err
	}
	key, err := os.ReadFile(filepath.Join(s.dataDir, secretbox.KeyFile))
	if err != nil {
		return Manifest{}, fmt.Errorf("read secret key: %w", err)
	}
	files, err := stackFiles(s.stacksDir)
	if err != nil {
		return Manifest{}, err
	}
	schema, err := store.SchemaVersion()
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Format: format, Version: s.version, Schema: schema, Created: s.now().UTC(), Stacks: countStacks(files)}

	enc, err := encrypt(w, passphrase, s.kdf)
	if err != nil {
		return Manifest{}, err
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	manifest, _ := json.Marshal(m)
	if err := addBytes(tw, manifestName, manifest, m.Created); err != nil {
		return Manifest{}, err
	}
	if err := addFile(tw, databaseName, db, m.Created); err != nil {
		return Manifest{}, err
	}
	if err := addBytes(tw, keyName, key, m.Created); err != nil {
		return Manifest{}, err
	}
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		if err := addFile(tw, stacksPrefix+rel, filepath.Join(s.stacksDir, filepath.FromSlash(rel)), m.Created); err != nil {
			return Manifest{}, err
		}
	}
	for _, c := range []io.Closer{tw, gz, enc} {
		if err := c.Close(); err != nil {
			return Manifest{}, fmt.Errorf("write backup: %w", err)
		}
	}
	return m, nil
}

// stackFile matches a stack's file below the stacks directory; nothing else there (a
// stack's bind-mounted data, the checker's drafts) goes into a backup.
var stackFile = regexp.MustCompile(`^[0-9]+/([^/]+)/(compose\.yaml|\.env)$`)

func validStackFile(rel string) bool {
	m := stackFile.FindStringSubmatch(rel)
	return m != nil && stacks.ValidName(m[1])
}

// stackFiles lists the stacks' compose.yaml and .env files, relative to dir with
// slashes, sorted; a missing dir has none.
func stackFiles(dir string) ([]string, error) {
	var files []string
	envs, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read stacks: %w", err)
	}
	for _, env := range envs {
		if !env.IsDir() {
			continue
		}
		names, err := os.ReadDir(filepath.Join(dir, env.Name()))
		if err != nil {
			return nil, fmt.Errorf("read stacks: %w", err)
		}
		for _, name := range names {
			for _, f := range []string{stacks.ComposeFile, stacks.EnvFile} {
				rel := path.Join(env.Name(), name.Name(), f)
				if !name.IsDir() || !validStackFile(rel) {
					continue
				}
				info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					return nil, fmt.Errorf("read stacks: %w", err)
				}
				if info.Mode().IsRegular() {
					files = append(files, rel)
				}
			}
		}
	}
	slices.Sort(files)
	return files, nil
}

// countStacks counts the stacks with a compose file.
func countStacks(files []string) int {
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "/"+stacks.ComposeFile) {
			n++
		}
	}
	return n
}

func addBytes(tw *tar.Writer, name string, b []byte, t time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: t, Typeflag: tar.TypeReg}); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	if _, err := tw.Write(b); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	return nil
}

func addFile(tw *tar.Writer, name, src string, t time.Time) error {
	f, err := os.Open(src) // #nosec G304 -- the snapshot or a stack file stackFiles listed
	if err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size(), ModTime: t, Typeflag: tar.TypeReg}); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	// A stack file written meanwhile is replaced by a rename, so this file keeps its size.
	if _, err := io.CopyN(tw, f, info.Size()); err != nil {
		return fmt.Errorf("write backup %s: %w", name, err)
	}
	return nil
}

// ReadPassphrase reads a passphrase from a file (a Docker secret, say), without the
// trailing newline an editor or echo leaves.
func ReadPassphrase(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the operator's configured passphrase file
	if err != nil {
		return "", fmt.Errorf("backup passphrase: %w", err)
	}
	p := strings.TrimRight(string(b), "\r\n")
	if len([]rune(p)) < MinPassphrase {
		return "", fmt.Errorf("backup passphrase in %s: %w", path, ErrPassphrase)
	}
	return p, nil
}
