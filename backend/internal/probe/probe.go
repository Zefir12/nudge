// Package probe runs a deployment's HTTP check from inside the environment: a short-lived
// container from nudge's own image joins the target container's network namespace and
// runs `nudge probe` against 127.0.0.1, so the check needs no published port, no shared
// network and no HTTP client in the target's image.
package probe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Exit codes of `nudge probe`.
const (
	ExitOK        = 0
	ExitStatus    = 3
	ExitNoAnswer  = 4
	ExitBadInput  = 2
	MaxTimeout    = time.Minute
	maxPathLength = 512
	// Label marks the helper containers.
	Label = "nudge.probe"
)

// pollEvery is the pause between looks at a running helper; slack is how long the
// helper may take beyond the check's timeout (create, start, exit).
const (
	pollEvery = 250 * time.Millisecond
	slack     = 15 * time.Second
)

// ErrNoSelf means nudge doesn't run in a container of the environment's daemon, so it
// has no image to start the helper from.
var ErrNoSelf = errors.New("nudge doesn't run in a container on this host, so it can't start an HTTP check")

// FailedError is a check that ran and failed; Msg is safe to show.
type FailedError struct{ Msg string }

func (e *FailedError) Error() string { return e.Msg }

// Request is one HTTP check.
type Request struct {
	// Container is the ID of the container whose network namespace the check uses.
	Container string        `json:"container"`
	Port      int           `json:"port"`
	Path      string        `json:"path"`
	Status    int           `json:"status"`
	Timeout   time.Duration `json:"timeout"`
}

var (
	pathPattern = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/?-]*$`)
	idPattern   = regexp.MustCompile(`^[a-f0-9]{12,64}$`)
)

// ValidPath reports whether p can be a check's path: absolute, URL characters only.
func ValidPath(p string) bool { return len(p) <= maxPathLength && pathPattern.MatchString(p) }

// Validate checks r's fields.
func (r Request) Validate() error {
	switch {
	case !idPattern.MatchString(r.Container):
		return errors.New("invalid container ID")
	case r.Port < 1 || r.Port > 65535:
		return errors.New("port must be 1 to 65535")
	case !ValidPath(r.Path):
		return errors.New("path must start with / and hold only URL characters")
	case r.Status < 100 || r.Status > 599:
		return errors.New("status must be 100 to 599")
	case r.Timeout <= 0 || r.Timeout > MaxTimeout:
		return fmt.Errorf("timeout must be above 0 and at most %s", MaxTimeout)
	}
	return nil
}

// URL is what the helper requests.
func (r Request) URL() string { return "http://127.0.0.1:" + strconv.Itoa(r.Port) + r.Path }

// Docker is what a Prober needs of the environment's daemon.
type Docker interface {
	ContainerInspect(ctx context.Context, id string, opts client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}

// Options configure a Prober.
type Options struct {
	// Docker returns the client of the daemon nudge's container runs on.
	Docker func() (Docker, error)
	// Self is the ID of nudge's container and Hostname this process's hostname
	// (stacks.SelfID); Self is empty outside a container.
	Self     string
	Hostname string
	// After is time.After outside tests.
	After func(time.Duration) <-chan time.Time
}

// Prober runs checks through helper containers.
type Prober struct {
	docker   func() (Docker, error)
	self     string
	hostname string
	after    func(time.Duration) <-chan time.Time
}

// New returns a Prober.
func New(opts Options) *Prober {
	p := &Prober{docker: opts.Docker, self: opts.Self, hostname: opts.Hostname, after: opts.After}
	if p.after == nil {
		p.after = time.After
	}
	return p
}

// ProjectLabel is compose's label naming a container's project (stacks.ProjectLabel).
const ProjectLabel = "com.docker.compose.project"

// Run runs r against a container of compose project project and returns nil if it
// answered r.Status, a *FailedError if it didn't, or another error if the check
// couldn't run.
func (p *Prober) Run(ctx context.Context, project string, r Request) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("http check: %w", err)
	}
	if p.self == "" {
		return ErrNoSelf
	}
	cli, err := p.docker()
	if err != nil {
		return fmt.Errorf("http check: %w", err)
	}
	target, err := cli.ContainerInspect(ctx, r.Container, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("http check: inspect target: %w", err)
	}
	if target.Container.Config == nil || target.Container.Config.Labels[ProjectLabel] != project {
		return &FailedError{Msg: "the container isn't one of the stack's"}
	}
	self, err := cli.ContainerInspect(ctx, p.self, client.ContainerInspectOptions{})
	if err != nil || self.Container.Image == "" || self.Container.Config == nil || self.Container.Config.Hostname != p.hostname {
		// Not ours: nudge runs in a container of another daemon (as stacks.ContainerMounts).
		return ErrNoSelf
	}
	res, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  self.Container.Image,
			Cmd:    []string{"probe", "-status", strconv.Itoa(r.Status), "-timeout", r.Timeout.String(), r.URL()},
			Labels: map[string]string{Label: "1"},
		},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("container:" + r.Container)},
	})
	if err != nil {
		return fmt.Errorf("http check: create helper: %w", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = cli.ContainerRemove(ctx, res.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("http check: start helper: %w", err)
	}
	code, err := p.wait(ctx, cli, res.ID, r.Timeout+slack)
	if err != nil {
		return err
	}
	switch code {
	case ExitOK:
		return nil
	case ExitStatus:
		return &FailedError{Msg: fmt.Sprintf("answered another status than %d", r.Status)}
	case ExitNoAnswer:
		return &FailedError{Msg: fmt.Sprintf("didn't answer on port %d within %s", r.Port, r.Timeout)}
	default:
		return fmt.Errorf("http check: helper exited with code %d", code)
	}
}

// wait returns the exit code of helper id once it stops.
func (p *Prober) wait(ctx context.Context, cli Docker, id string, limit time.Duration) (int, error) {
	deadline := p.after(limit)
	for {
		info, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return 0, fmt.Errorf("http check: inspect helper: %w", err)
		}
		if st := info.Container.State; st != nil && (st.Status == container.StateExited || st.Status == container.StateDead) {
			return st.ExitCode, nil
		}
		select {
		case <-p.after(pollEvery):
		case <-deadline:
			return 0, &FailedError{Msg: "the check didn't finish in time"}
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// Check is the helper's side: one GET of url without following redirects; it returns
// the exit code and a line for the log.
func Check(ctx context.Context, url string, status int, timeout time.Duration) (int, string) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ExitBadInput, "invalid URL"
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return ExitNoAnswer, "no answer: " + err.Error()
	}
	_ = resp.Body.Close()
	if resp.StatusCode != status {
		return ExitStatus, fmt.Sprintf("status %d, want %d", resp.StatusCode, status)
	}
	return ExitOK, fmt.Sprintf("status %d", resp.StatusCode)
}
