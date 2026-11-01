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
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/metrics"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

// ErrNotEmpty means the data directory already has a database, key or stacks, which a
// restore without Force leaves alone.
var ErrNotEmpty = errors.New("the data directory already has nudge data")

// maxManifest bounds the manifest a restore reads into memory.
const maxManifest = 64 << 10

// maxKey and maxStackFile bound those entries; the database has no bound but its size
// in the tar header.
const (
	maxKey       = 1 << 10
	maxStackFile = 4 << 20
)

// maxTrailer bounds what may follow the tar's end inside the gzip stream (tar pads its
// end to a block of 10 KiB).
const maxTrailer = 64 << 10

// RestoreOptions configure Restore.
type RestoreOptions struct {
	DataDir    string
	StacksDir  string
	Passphrase string
	// Force moves existing data aside (<data>/pre-restore-<time>/, and the stacks'
	// compose files to <stacks>/.pre-restore-<time>/) instead of refusing.
	Force bool
	// Now is time.Now outside tests: the moved-aside directories' names.
	Now func() time.Time
	// rename is os.Rename outside tests.
	rename func(oldpath, newpath string) error
}

// Restore unpacks the backup read from r into the data and stacks directories. The
// server must be stopped. Everything is unpacked next to its destination before anything
// is replaced, so a damaged archive, a wrong passphrase or one from a newer nudge changes
// nothing; if putting the files in place fails, the moves made are undone. Only files
// move: the stacks directory may be a mount point, and what stacks bind-mount from their
// directories stays where it is. It returns the archive's manifest and, with Force,
// where the old data went.
func Restore(ctx context.Context, r io.Reader, o RestoreOptions) (_ Manifest, _ string, err error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.rename == nil {
		o.rename = os.Rename
	}
	var oldStacks []string
	oldStacks, err = stackFiles(o.StacksDir)
	if err != nil {
		return Manifest{}, "", err
	}
	existing, err := hasData(o.DataDir)
	if err != nil {
		return Manifest{}, "", err
	}
	existing = existing || len(oldStacks) > 0
	if existing && !o.Force {
		return Manifest{}, "", ErrNotEmpty
	}
	for _, dir := range []string{o.DataDir, o.StacksDir} {
		if _, statErr := os.Stat(dir); errors.Is(statErr, fs.ErrNotExist) {
			// A failed restore leaves no new directories behind.
			defer func() {
				if err != nil {
					_ = os.Remove(dir)
				}
			}()
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Manifest{}, "", fmt.Errorf("restore: %w", err)
		}
	}
	stage, err := os.MkdirTemp(o.DataDir, ".restore-*")
	if err != nil {
		return Manifest{}, "", fmt.Errorf("restore: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	// Staged inside the stacks directory, which may be another volume than the data.
	stageStacks, err := os.MkdirTemp(o.StacksDir, ".restore-*")
	if err != nil {
		return Manifest{}, "", fmt.Errorf("restore: %w", err)
	}
	defer func() { _ = os.RemoveAll(stageStacks) }()

	m, err := unpack(ctx, r, o.Passphrase, stage, stageStacks)
	if err != nil {
		return Manifest{}, "", err
	}
	newStacks, err := stackFiles(stageStacks)
	if err != nil {
		return Manifest{}, "", err
	}

	mv := &mover{rename: o.rename}
	var aside, stacksAside string
	if existing {
		suffix := "pre-restore-" + o.Now().UTC().Format("20060102-150405")
		aside, stacksAside = filepath.Join(o.DataDir, suffix), filepath.Join(o.StacksDir, "."+suffix)
		for _, name := range dataFiles {
			if err := mv.move(filepath.Join(o.DataDir, name), filepath.Join(aside, name)); err != nil {
				return Manifest{}, "", mv.fail("move old data aside", err, aside, stacksAside)
			}
		}
		for _, rel := range oldStacks {
			if err := mv.move(filepath.Join(o.StacksDir, rel), filepath.Join(stacksAside, rel)); err != nil {
				return Manifest{}, "", mv.fail("move old stacks aside", err, aside, stacksAside)
			}
		}
	}
	for _, name := range []string{secretbox.KeyFile, store.FileName} {
		if err := mv.move(filepath.Join(stage, name), filepath.Join(o.DataDir, name)); err != nil {
			return Manifest{}, "", mv.fail("restore "+name, err, aside, stacksAside)
		}
	}
	for _, rel := range newStacks {
		if err := mv.move(filepath.Join(stageStacks, rel), filepath.Join(o.StacksDir, rel)); err != nil {
			return Manifest{}, "", mv.fail("restore stacks", err, aside, stacksAside)
		}
	}
	return m, aside, nil
}

// dataFiles are what a restore replaces in the data directory: the database with its WAL
// files, and the key. The metrics database is not in backups but goes aside too: its
// rows name the old database's environments.
var dataFiles = []string{
	store.FileName, store.FileName + "-wal", store.FileName + "-shm", secretbox.KeyFile,
	metrics.FileName, metrics.FileName + "-wal", metrics.FileName + "-shm",
}

func hasData(dataDir string) (bool, error) {
	for _, name := range dataFiles {
		if _, err := os.Lstat(filepath.Join(dataDir, name)); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("restore: %w", err)
		}
	}
	return false, nil
}

// mover renames files, creating the directories they go to, and remembers the renames
// so a failure can undo them.
type mover struct {
	rename func(oldpath, newpath string) error
	done   [][2]string
	// made are the directories move created, outermost first.
	made []string
}

// move renames from to to; a from that doesn't exist is skipped.
func (m *mover) move(from, to string) error {
	if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var missing []string
	for dir := filepath.Dir(to); ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(dir); err == nil || dir == filepath.Dir(dir) {
			break
		}
		missing = append(missing, dir)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		m.made = append(m.made, missing[i])
	}
	if err := m.rename(from, to); err != nil {
		return err
	}
	m.done = append(m.done, [2]string{from, to})
	return nil
}

// fail undoes the renames, newest first, removes the directories they made, and words
// err. If undoing fails too, the error says where the old data is.
func (m *mover) fail(what string, err error, asideDirs ...string) error {
	var undoErr error
	for i := len(m.done) - 1; i >= 0; i-- {
		if e := m.rename(m.done[i][1], m.done[i][0]); e != nil && undoErr == nil {
			undoErr = e
		}
	}
	if undoErr != nil {
		return fmt.Errorf("%s: %w; undoing the restore failed too (%w): the old data is in %s", what, err, undoErr, strings.Join(asideDirs, " and "))
	}
	// Innermost first; os.Remove leaves a directory that isn't empty.
	for i := len(m.made) - 1; i >= 0; i-- {
		_ = os.Remove(m.made[i])
	}
	return fmt.Errorf("%s: %w; nothing was changed", what, err)
}

// unpack decrypts and checks the archive, writing the database and key into stage and
// the stack files into stageStacks. Only the names a backup writes are accepted.
func unpack(ctx context.Context, r io.Reader, passphrase, stage, stageStacks string) (Manifest, error) {
	plain, err := decrypt(r, passphrase)
	if err != nil {
		return Manifest{}, err
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		return Manifest{}, damaged(err)
	}
	tr := tar.NewReader(gz)

	hdr, err := tr.Next()
	if err != nil {
		return Manifest{}, damaged(err)
	}
	if hdr.Name != manifestName || hdr.Typeflag != tar.TypeReg || hdr.Size > maxManifest {
		return Manifest{}, errors.New("not a nudge backup: no manifest")
	}
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(tr, maxManifest)).Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("not a nudge backup: manifest: %w", err)
	}
	if m.Format != format {
		return Manifest{}, fmt.Errorf("backup format %d is not supported by this nudge", m.Format)
	}
	schema, err := store.SchemaVersion()
	if err != nil {
		return Manifest{}, err
	}
	if m.Schema > schema {
		return Manifest{}, fmt.Errorf("the backup is from a newer nudge (%s, schema %d; this one knows up to %d): restore it with that version or newer", m.Version, m.Schema, schema)
	}

	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, damaged(err)
		}
		if hdr.Typeflag != tar.TypeReg || seen[hdr.Name] {
			return Manifest{}, fmt.Errorf("unexpected entry %q in the backup", hdr.Name)
		}
		seen[hdr.Name] = true
		var dst string
		limit := int64(-1)
		switch {
		case hdr.Name == databaseName:
			dst = filepath.Join(stage, store.FileName)
		case hdr.Name == keyName:
			dst, limit = filepath.Join(stage, secretbox.KeyFile), maxKey
		case strings.HasPrefix(hdr.Name, stacksPrefix) && validStackFile(strings.TrimPrefix(hdr.Name, stacksPrefix)):
			rel := filepath.FromSlash(strings.TrimPrefix(hdr.Name, stacksPrefix))
			dst, limit = filepath.Join(stageStacks, rel), maxStackFile
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return Manifest{}, fmt.Errorf("restore: %w", err)
			}
		default:
			return Manifest{}, fmt.Errorf("unexpected entry %q in the backup", hdr.Name)
		}
		if limit >= 0 && hdr.Size > limit {
			return Manifest{}, fmt.Errorf("entry %q in the backup is too large", hdr.Name)
		}
		if err := writeEntry(dst, tr); err != nil {
			return Manifest{}, err
		}
	}
	// Read to the end: the gzip checksum and the last chunk's flag catch a cut archive.
	// After the tar's end only its zero padding is left.
	if n, err := io.Copy(io.Discard, io.LimitReader(gz, maxTrailer+1)); err != nil {
		return Manifest{}, damaged(err)
	} else if n > maxTrailer {
		return Manifest{}, errors.New("the backup is damaged: unexpected data after the archive")
	}
	if _, err := io.Copy(io.Discard, plain); err != nil {
		return Manifest{}, damaged(err)
	}
	if !seen[databaseName] || !seen[keyName] {
		return Manifest{}, errors.New("the backup has no database or no secret key")
	}
	if _, err := secretbox.LoadKey(stage); err != nil {
		return Manifest{}, fmt.Errorf("the backup's secret key: %w", err)
	}
	return m, nil
}

func writeEntry(dst string, r io.Reader) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- a fixed name or a checked stack file below the staging directory
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return damaged(err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("restore: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	return nil
}

// damaged keeps ErrDecrypt (wrong passphrase, cut file) and words other read errors.
func damaged(err error) error {
	if errors.Is(err, ErrDecrypt) {
		return err
	}
	return fmt.Errorf("the backup is damaged: %w", err)
}
