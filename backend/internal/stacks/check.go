package stacks

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

// maxCheckErrors bounds the error lines Check answers.
const maxCheckErrors = 20

// CheckerOptions configure a Checker.
type CheckerOptions struct {
	// TempDir is where drafts are written while compose reads them; it is created if
	// missing.
	TempDir string
	// Compose is the docker-compose binary; empty means DefaultCompose.
	Compose string
	// Exec runs compose; nil means as a subprocess.
	Exec ExecFunc
}

// Checker validates stack files with `docker-compose config`, which reads them like a
// deploy would (schema, interpolation with .env) without talking to a daemon.
type Checker struct {
	tmp     string
	compose string
	exec    ExecFunc
}

// NewChecker returns a checker for opts.
func NewChecker(opts CheckerOptions) *Checker {
	c := &Checker{tmp: opts.TempDir, compose: opts.Compose, exec: opts.Exec}
	if c.compose == "" {
		c.compose = DefaultCompose
	}
	if c.exec == nil {
		c.exec = runCommand
	}
	return c
}

// Check returns compose's complaints about f, none if compose accepts it. Files the
// compose file names relative to the stack (env_file, bind sources) aren't checked: a
// draft has no directory of its own.
func (c *Checker) Check(ctx context.Context, f Files) ([]string, error) {
	if err := os.MkdirAll(c.tmp, 0o700); err != nil {
		return nil, fmt.Errorf("check stack: %w", err)
	}
	tmp, err := os.MkdirTemp(c.tmp, ".check-")
	if err != nil {
		return nil, fmt.Errorf("check stack: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	const name = "draft"
	d := NewDir(tmp)
	if err := d.Create(name, f); err != nil {
		return nil, fmt.Errorf("check stack: %w", err)
	}
	dir := d.Path(name)
	var (
		mu   sync.Mutex
		errs []string
	)
	collect := func(l Line) {
		mu.Lock()
		defer mu.Unlock()
		// Compose names the draft's path; it means nothing to the user.
		text := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(l.Text, dir+"/", ""), dir, "."))
		if text != "" && len(errs) < maxCheckErrors {
			errs = append(errs, text)
		}
	}
	stdout, stderr := &lineWriter{stream: "stdout", emit: collect}, &lineWriter{stream: "stderr", emit: collect}
	code, err := c.exec(ctx, Command{
		Path: c.compose,
		Args: []string{
			"--project-name", name, "--project-directory", dir, "--file", dir + "/" + ComposeFile,
			"--ansi", "never", "config", "--quiet", "--no-env-resolution", "--no-path-resolution",
		},
		Env: []string{"HOME=" + os.TempDir(), "PATH=/usr/local/bin:/usr/bin:/bin"},
		Dir: dir, Stdout: stdout, Stderr: stderr,
	})
	stdout.flush()
	stderr.flush()
	if err != nil {
		return nil, fmt.Errorf("check stack: %w", err)
	}
	if code == 0 {
		return []string{}, nil
	}
	if len(errs) == 0 {
		errs = append(errs, fmt.Sprintf("compose rejected the file (exit code %d)", code))
	}
	return errs, nil
}
