package stacks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"
)

// DefaultCompose is where the image has the docker-compose binary.
const DefaultCompose = "/usr/local/bin/docker-compose"

// maxLine bounds one output line; longer ones are split.
const maxLine = 8 << 10

// waitDelay is how long a killed compose has to close its output.
const waitDelay = 5 * time.Second

// ErrNoCompose means the compose binary isn't there.
var ErrNoCompose = errors.New("docker-compose is not installed")

// Command is one compose run.
type Command struct {
	Path string
	Args []string
	// Env is the whole environment: nothing of nudge's own is passed on.
	Env    []string
	Dir    string
	Stdout io.Writer
	Stderr io.Writer
}

// ExecFunc runs cmd until it exits or ctx ends, returning its exit code; an error means
// it didn't run or was killed.
type ExecFunc func(ctx context.Context, cmd Command) (int, error)

// LocalOptions configure a Local runner.
type LocalOptions struct {
	// Dir holds the stacks the runner deploys.
	Dir Dir
	// Compose is the docker-compose binary; empty means DefaultCompose.
	Compose string
	// DockerHost is the daemon compose talks to, as DOCKER_HOST.
	DockerHost string
	// Exec runs compose; nil means as a subprocess.
	Exec ExecFunc
	// Credentials finds the registry credentials of a stack's `compose up`; nil means
	// none (an agent's runner gets them from the server with each request).
	Credentials CredentialFunc
}

// Local runs compose here, on stacks in a directory of this machine.
type Local struct {
	dir         Dir
	compose     string
	dockerHost  string
	exec        ExecFunc
	credentials CredentialFunc
}

// NewLocal returns a runner for the stacks in opts.Dir.
func NewLocal(opts LocalOptions) *Local {
	l := &Local{dir: opts.Dir, compose: opts.Compose, dockerHost: opts.DockerHost, exec: opts.Exec, credentials: opts.Credentials}
	if l.compose == "" {
		l.compose = DefaultCompose
	}
	if l.exec == nil {
		l.exec = runCommand
	}
	return l
}

var _ Runner = (*Local)(nil)

// Sync writes f into the stack's directory.
func (l *Local) Sync(_ context.Context, name string, f Files) error {
	return l.dir.Write(name, f)
}

// Up runs `up -d --remove-orphans`, with `--pull always` when pull is set.
func (l *Local) Up(ctx context.Context, name string, pull bool, out func(Line)) (int, error) {
	args := []string{"up", "--detach", "--remove-orphans"}
	if pull {
		args = append(args, "--pull", "always")
	}
	return l.run(ctx, name, args, out)
}

// Down runs `down --remove-orphans`, with `--volumes` when volumes is set.
func (l *Local) Down(ctx context.Context, name string, volumes bool, out func(Line)) (int, error) {
	args := []string{"down", "--remove-orphans"}
	if volumes {
		args = append(args, "--volumes")
	}
	return l.run(ctx, name, args, out)
}

// Remove deletes the stack's directory.
func (l *Local) Remove(_ context.Context, name string) error {
	return l.dir.Remove(name)
}

// run runs compose on stack name with fixed global arguments and then args; `up` gets
// the registry credentials it may need.
func (l *Local) run(ctx context.Context, name string, args []string, out func(Line)) (code int, err error) {
	files, err := l.dir.Read(name)
	if err != nil {
		return 0, err
	}
	dir := l.dir.Path(name)
	global := []string{
		"--project-name", name, "--project-directory", dir, "--file", dir + "/" + ComposeFile,
		"--ansi", "never", "--progress", "plain",
	}
	env := []string{"DOCKER_HOST=" + l.dockerHost, "HOME=" + os.TempDir(), "PATH=/usr/local/bin:/usr/bin:/bin"}
	var secrets []string
	if args[0] == "up" {
		creds, credErr := l.credentialsFor(ctx, files)
		if credErr != nil {
			return 0, fmt.Errorf("compose up %s: %w", name, credErr)
		}
		if len(creds) > 0 {
			config, cfgErr := writeDockerConfig(creds)
			if cfgErr != nil {
				return 0, fmt.Errorf("compose up %s: %w", name, cfgErr)
			}
			// err is run's result here, so a config left on disk fails the run.
			defer func() {
				if rmErr := os.RemoveAll(config); rmErr != nil && err == nil {
					code, err = 0, fmt.Errorf("compose up %s: remove docker config: %w", name, rmErr)
				}
			}()
			env = append(env, "DOCKER_CONFIG="+config)
			secrets = secretsOf(creds)
		}
	}
	var mu sync.Mutex
	emit := func(line Line) {
		line.Text = mask(line.Text, secrets)
		mu.Lock()
		defer mu.Unlock()
		out(line)
	}
	stdout, stderr := &lineWriter{stream: "stdout", emit: emit}, &lineWriter{stream: "stderr", emit: emit}
	code, err = l.exec(ctx, Command{
		Path: l.compose, Args: append(global, args...), Dir: dir, Env: env,
		Stdout: stdout, Stderr: stderr,
	})
	stdout.flush()
	stderr.flush()
	if err != nil {
		return 0, fmt.Errorf("compose %s %s: %w", args[0], name, err)
	}
	return code, nil
}

// credentialsFor returns the credentials handed over in ctx and those the Credentials
// option finds for files.
func (l *Local) credentialsFor(ctx context.Context, files Files) ([]Credential, error) {
	creds := credentialsFrom(ctx)
	if l.credentials == nil {
		return creds, nil
	}
	found, err := l.credentials(ctx, files)
	if err != nil {
		return nil, fmt.Errorf("registry credentials: %w", err)
	}
	return append(creds[:len(creds):len(creds)], found...), nil
}

// runCommand runs cmd as a subprocess, killed when ctx ends.
func runCommand(ctx context.Context, cmd Command) (int, error) {
	if _, err := os.Stat(cmd.Path); err != nil {
		return 0, ErrNoCompose
	}
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...) // #nosec G204 G702 -- fixed binary, arguments built by Local from a ValidName name
	c.Env, c.Dir, c.Stdout, c.Stderr = cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr
	c.WaitDelay = waitDelay
	err := c.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() < 0:
		// Killed by a signal: by ctx, or from outside (OOM).
		return 0, err
	case errors.As(err, &exit):
		return exit.ExitCode(), nil
	case errors.Is(err, exec.ErrWaitDelay):
		// compose exited but something it started kept its output open.
		return c.ProcessState.ExitCode(), nil
	case err != nil:
		return 0, err
	}
	return 0, nil
}

// lineWriter turns writes into lines, splitting ones longer than maxLine.
type lineWriter struct {
	stream string
	emit   func(Line)
	buf    []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			break
		}
		w.buf = append(w.buf, p[:i]...)
		w.flush()
		p = p[i+1:]
	}
	w.buf = append(w.buf, p...)
	w.split()
	return n, nil
}

// split emits maxLine pieces of the buffer while it is longer than that, cut between
// runes.
func (w *lineWriter) split() {
	for len(w.buf) > maxLine {
		cut := maxLine
		for cut > maxLine-utf8.UTFMax && !utf8.RuneStart(w.buf[cut]) {
			cut--
		}
		w.emit(Line{Stream: w.stream, Text: string(w.buf[:cut])})
		w.buf = w.buf[cut:]
	}
}

// flush emits what is buffered as a line (blank lines are dropped).
func (w *lineWriter) flush() {
	w.split()
	if text := bytes.TrimSuffix(w.buf, []byte("\r")); len(text) > 0 {
		w.emit(Line{Stream: w.stream, Text: string(text)})
	}
	w.buf = w.buf[:0]
}
