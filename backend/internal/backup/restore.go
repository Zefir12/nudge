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

	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

// ErrNotEmpty means the data directory already has a database, key or stacks, which a
// restore without Force leaves alone.
var ErrNotEmpty = errors.New("the data directory already has nudge data")

// maxManifest bounds the manifest a restore reads into memory.
const maxManifest = 64 << 10

// maxTrailer bounds what may follow the tar's end inside the gzip stream (tar pads its
// end to a block of 10 KiB).
const maxTrailer = 64 << 10

// RestoreOptions configure Restore.
type RestoreOptions struct {
	DataDir    string
	StacksDir  string
	Passphrase string
	// Force moves existing data aside (<data>/pre-restore-<time>/, and the stacks
	// directory to <stacks>.pre-restore-<time>) instead of refusing.
	Force bool
	// Now is time.Now outside tests: the moved-aside directories' names.
	Now func() time.Time
}

// Restore unpacks the backup read from r into the data and stacks directories. The
// server must be stopped. Everything is unpacked next to its destination before anything
// is replaced, so a damaged archive, a wrong passphrase or one from a newer nudge changes
// nothing. It returns the archive's manifest and, with Force, where the old data went.
func Restore(ctx context.Context, r io.Reader, o RestoreOptions) (Manifest, string, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	existing, err := hasData(o.DataDir, o.StacksDir)
	if err != nil {
		return Manifest{}, "", err
	}
	if existing && !o.Force {
		return Manifest{}, "", ErrNotEmpty
	}
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return Manifest{}, "", fmt.Errorf("create data directory: %w", err)
	}
	stage, err := os.MkdirTemp(o.DataDir, ".restore-*")
	if err != nil {
		return Manifest{}, "", fmt.Errorf("restore: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	// The stacks are staged next to the stacks directory, which may be on another volume.
	stacksParent := filepath.Dir(o.StacksDir)
	if err := os.MkdirAll(stacksParent, 0o700); err != nil {
		return Manifest{}, "", fmt.Errorf("create stacks directory: %w", err)
	}
	stageStacks, err := os.MkdirTemp(stacksParent, ".restore-stacks-*")
	if err != nil {
		return Manifest{}, "", fmt.Errorf("restore: %w", err)
	}
	defer func() { _ = os.RemoveAll(stageStacks) }()

	m, err := unpack(ctx, r, o.Passphrase, stage, stageStacks)
	if err != nil {
		return Manifest{}, "", err
	}

	var aside string
	if existing {
		if aside, err = moveAside(o.DataDir, o.StacksDir, o.Now()); err != nil {
			return Manifest{}, "", err
		}
	}
	for _, name := range []string{databaseName, secretbox.KeyFile} {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join(o.DataDir, name)); err != nil {
			return Manifest{}, aside, fmt.Errorf("restore %s: %w", name, err)
		}
	}
	if err := os.Rename(stageStacks, o.StacksDir); err != nil {
		return Manifest{}, aside, fmt.Errorf("restore stacks: %w", err)
	}
	return m, aside, nil
}

// dataFiles are what a restore replaces in the data directory: the database with its WAL
// files, and the key.
var dataFiles = []string{store.FileName, store.FileName + "-wal", store.FileName + "-shm", secretbox.KeyFile}

func hasData(dataDir, stacksDir string) (bool, error) {
	for _, name := range dataFiles {
		if _, err := os.Lstat(filepath.Join(dataDir, name)); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("restore: %w", err)
		}
	}
	entries, err := os.ReadDir(stacksDir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("restore: %w", err)
	}
	return len(entries) > 0, nil
}

// moveAside moves the existing data files into <data>/pre-restore-<time>/ and the stacks
// directory to <stacks>.pre-restore-<time>, and returns the first.
func moveAside(dataDir, stacksDir string, now time.Time) (string, error) {
	suffix := "pre-restore-" + now.UTC().Format("20060102-150405")
	aside := filepath.Join(dataDir, suffix)
	if err := os.Mkdir(aside, 0o700); err != nil {
		return "", fmt.Errorf("move old data aside: %w", err)
	}
	for _, name := range dataFiles {
		err := os.Rename(filepath.Join(dataDir, name), filepath.Join(aside, name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return aside, fmt.Errorf("move old data aside: %w", err)
		}
	}
	err := os.Rename(stacksDir, stacksDir+"."+suffix)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return aside, fmt.Errorf("move old stacks aside: %w", err)
	}
	return aside, nil
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
		switch {
		case hdr.Name == databaseName:
			dst = filepath.Join(stage, store.FileName)
		case hdr.Name == keyName:
			dst = filepath.Join(stage, secretbox.KeyFile)
		case strings.HasPrefix(hdr.Name, stacksPrefix) && validStackFile(strings.TrimPrefix(hdr.Name, stacksPrefix)):
			rel := filepath.FromSlash(strings.TrimPrefix(hdr.Name, stacksPrefix))
			dst = filepath.Join(stageStacks, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return Manifest{}, fmt.Errorf("restore: %w", err)
			}
		default:
			return Manifest{}, fmt.Errorf("unexpected entry %q in the backup", hdr.Name)
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
