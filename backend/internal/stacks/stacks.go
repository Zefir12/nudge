// Package stacks keeps compose projects as files and runs docker-compose on them.
// docs/architecture.md (Stacks) has the design: the server holds the files of every
// stack, and compose runs next to the daemon it deploys to.
package stacks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// File names inside a stack's directory.
const (
	ComposeFile = "compose.yaml"
	EnvFile     = ".env"
)

// MaxNameLength bounds a stack name, which is also its compose project name.
const MaxNameLength = 63

// ProjectLabel is the label compose puts on every container of a project.
const ProjectLabel = "com.docker.compose.project"

// ServiceLabel is the label compose puts on a container with its service's name.
const ServiceLabel = "com.docker.compose.service"

var (
	// ErrNotFound means the stack has no directory.
	ErrNotFound = errors.New("stack not found")
	// ErrExists means a stack of that name already exists.
	ErrExists = errors.New("stack already exists")
)

// name follows compose's project name rules.
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidName reports whether s can name a stack: a compose project name, which is also a
// directory name, so it can't escape the stacks directory.
func ValidName(s string) bool {
	return len(s) <= MaxNameLength && name.MatchString(s)
}

// Files are what the UI edits of a stack.
type Files struct {
	// Compose is compose.yaml.
	Compose string
	// Env is .env, the variables compose interpolates; empty means no file.
	Env string
}

// Dir holds stacks as <root>/<name>/{compose.yaml,.env}.
type Dir struct {
	root string
}

// NewDir returns the stacks under root; the directory is created on the first write.
func NewDir(root string) Dir { return Dir{root: root} }

// Path is the directory of stack name, the compose project directory.
func (d Dir) Path(name string) string { return filepath.Join(d.root, name) }

// List returns the names of the stacks, sorted.
func (d Dir) List() ([]string, error) {
	entries, err := os.ReadDir(d.root)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list stacks: %w", err)
	}
	names := []string{}
	for _, e := range entries {
		if e.IsDir() && ValidName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Read returns the files of stack name.
func (d Dir) Read(name string) (Files, error) {
	if !ValidName(name) {
		return Files{}, ErrNotFound
	}
	compose, err := os.ReadFile(filepath.Join(d.Path(name), ComposeFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Files{}, ErrNotFound
	}
	if err != nil {
		return Files{}, fmt.Errorf("read stack %s: %w", name, err)
	}
	env, err := os.ReadFile(filepath.Join(d.Path(name), EnvFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Files{}, fmt.Errorf("read stack %s: %w", name, err)
	}
	return Files{Compose: string(compose), Env: string(env)}, nil
}

// Create writes a new stack; ErrExists if it has a directory already.
func (d Dir) Create(name string, f Files) error {
	if !ValidName(name) {
		return fmt.Errorf("stack name %q is invalid", name)
	}
	if err := os.MkdirAll(d.root, 0o700); err != nil {
		return fmt.Errorf("create stacks directory: %w", err)
	}
	err := os.Mkdir(d.Path(name), 0o700)
	if errors.Is(err, fs.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return fmt.Errorf("create stack %s: %w", name, err)
	}
	return d.write(name, f)
}

// Write replaces the files of stack name, creating it if needed (the agent's copy).
func (d Dir) Write(name string, f Files) error {
	if !ValidName(name) {
		return fmt.Errorf("stack name %q is invalid", name)
	}
	if err := os.MkdirAll(d.Path(name), 0o700); err != nil {
		return fmt.Errorf("create stack %s: %w", name, err)
	}
	return d.write(name, f)
}

// Update replaces the files of an existing stack; ErrNotFound if there is none.
func (d Dir) Update(name string, f Files) error {
	if _, err := d.Read(name); err != nil {
		return err
	}
	return d.write(name, f)
}

func (d Dir) write(name string, f Files) error {
	if err := writeFile(d.Path(name), ComposeFile, f.Compose); err != nil {
		return fmt.Errorf("write stack %s: %w", name, err)
	}
	if f.Env == "" {
		err := os.Remove(filepath.Join(d.Path(name), EnvFile))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("write stack %s: %w", name, err)
		}
		return nil
	}
	if err := writeFile(d.Path(name), EnvFile, f.Env); err != nil {
		return fmt.Errorf("write stack %s: %w", name, err)
	}
	return nil
}

// writeFile replaces dir/name with a rename, so compose never reads half a file.
func writeFile(dir, name, content string) error {
	tmp, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

// Remove deletes stack name and every file in its directory.
func (d Dir) Remove(name string) error {
	if _, err := d.Read(name); err != nil {
		return err
	}
	if err := os.RemoveAll(d.Path(name)); err != nil {
		return fmt.Errorf("remove stack %s: %w", name, err)
	}
	return nil
}

// Line is one line of compose's output.
type Line struct {
	// Stream is "stdout" or "stderr".
	Stream string
	Text   string
}

// Runner deploys stacks to one environment's daemon.
type Runner interface {
	// Sync makes the runner's copy of stack name match f.
	Sync(ctx context.Context, name string, f Files) error
	// Up runs compose up, with pull also pulling every image first, and returns
	// compose's exit code. out gets each output line, from one goroutine.
	Up(ctx context.Context, name string, pull bool, out func(Line)) (int, error)
	// Down runs compose down, with volumes also removing the project's volumes.
	Down(ctx context.Context, name string, volumes bool, out func(Line)) (int, error)
	// Remove deletes the runner's copy of stack name.
	Remove(ctx context.Context, name string) error
}
