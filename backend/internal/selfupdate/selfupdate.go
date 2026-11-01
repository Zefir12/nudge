// Package selfupdate replaces nudge's own container (the server's or an agent's) with one
// of a newer image. A process can't recreate the container it runs in, since stopping it
// ends the process halfway, so Launch starts a helper container from the new image and
// the helper's Swap does the replace, independent of the old process.
package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/config"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

// Label marks helper containers; its value is the ID of the container being replaced.
const Label = "net.zefirlabs.nudge.self-update"

// oldSuffix names the old container while the new one takes its name.
const oldSuffix = "-nudge-old"

var (
	// ErrNoSelf means nudge doesn't run in a container of this daemon.
	ErrNoSelf = errors.New("nudge doesn't run in a container on this host, so it can't update itself")
	// ErrNoSocket means nudge's container reaches Docker some other way than a
	// bind-mounted socket, which the helper would need too.
	ErrNoSocket = errors.New("nudge's container doesn't bind-mount the Docker socket, so it can't update itself")
	// ErrPinned means the container runs an image by ID or digest: nothing newer to pull.
	ErrPinned = errors.New("nudge's container runs an image by ID or digest, which has no newer version to pull")
	// ErrRunning means a helper is already replacing the container.
	ErrRunning = errors.New("an update of nudge is already running")
	// ErrAutoRemove means the container would be removed when stopped (docker run --rm).
	ErrAutoRemove = errors.New("nudge's container is removed when it stops (--rm), so it can't be replaced safely")
)

// Docker is what finding, launching and swapping need of the daemon.
type Docker interface {
	ContainerList(ctx context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerInspect(ctx context.Context, id string, opts client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerStop(ctx context.Context, id string, opts client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRename(ctx context.Context, id string, opts client.ContainerRenameOptions) (client.ContainerRenameResult, error)
	ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ImageInspect(ctx context.Context, id string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	Info(ctx context.Context, opts client.InfoOptions) (client.SystemInfoResult, error)
}

// Target is nudge's own container.
type Target struct {
	ID   string
	Name string
	// Image is the reference the container was created with, ImageID the image it runs.
	Image   string
	ImageID string
	// Project is its compose project, empty if it has none.
	Project string
	// socket is the host path of the Docker socket, mounted at socketPath inside.
	socket, socketPath string
}

// Find inspects self (stacks.SelfID) on the daemon. dockerHost is how this process
// reaches the daemon (unix:///var/run/docker.sock): the helper gets the same socket.
func Find(ctx context.Context, d Docker, self stacks.Self, dockerHost string) (Target, error) {
	if self.ID == "" {
		return Target{}, ErrNoSelf
	}
	res, err := d.ContainerInspect(ctx, self.ID, client.ContainerInspectOptions{})
	if docker.NotFound(err) {
		return Target{}, ErrNoSelf
	}
	if err != nil {
		return Target{}, fmt.Errorf("inspect own container: %w", err)
	}
	c := res.Container
	if c.Config == nil || c.HostConfig == nil || c.Config.Hostname != self.Hostname {
		// Not ours: nudge runs in a container of another daemon.
		return Target{}, ErrNoSelf
	}
	if c.HostConfig.AutoRemove {
		return Target{}, ErrAutoRemove
	}
	t := Target{
		ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), Image: c.Config.Image, ImageID: c.Image,
		Project: c.Config.Labels[stacks.ProjectLabel],
	}
	path, ok := strings.CutPrefix(dockerHost, "unix://")
	if !ok {
		return t, ErrNoSocket
	}
	for _, m := range c.Mounts {
		if m.Type == "bind" && m.Destination == path && m.Source != "" {
			t.socket, t.socketPath = m.Source, path
			return t, nil
		}
	}
	return t, ErrNoSocket
}

// Pinned reports whether ref names an image by ID or digest rather than by tag.
func Pinned(ref string) bool {
	return strings.Contains(ref, "@") || strings.HasPrefix(ref, "sha256:")
}

// Launch starts the helper that replaces t with a container of t.Image, which the caller
// has just pulled. The helper removes itself when done.
func Launch(ctx context.Context, d Docker, t Target) (string, error) {
	if t.socket == "" {
		return "", ErrNoSocket
	}
	if Pinned(t.Image) {
		return "", ErrPinned
	}
	running, err := d.ContainerList(ctx, client.ContainerListOptions{Filters: make(client.Filters).Add("label", Label+"="+t.ID)})
	if err != nil {
		return "", fmt.Errorf("list self-update helpers: %w", err)
	}
	if len(running.Items) > 0 {
		return "", ErrRunning
	}
	res, err := d.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  t.Image,
			Cmd:    []string{"self-update", "-docker-host", "unix://" + t.socketPath, t.ID},
			Labels: map[string]string{Label: t.ID},
		},
		HostConfig: &container.HostConfig{AutoRemove: true, Binds: []string{t.socket + ":" + t.socketPath}},
	})
	if err != nil {
		return "", fmt.Errorf("create self-update helper: %w", err)
	}
	if _, err := d.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = d.ContainerRemove(context.WithoutCancel(ctx), res.ID, client.ContainerRemoveOptions{Force: true})
		return "", fmt.Errorf("start self-update helper: %w", err)
	}
	return res.ID, nil
}

// SwapOptions tune Swap; zero values take the defaults.
type SwapOptions struct {
	Log *slog.Logger
	// After is time.After outside tests.
	After func(time.Duration) <-chan time.Time
	// Grace is the wait before the old container stops, so its answer to the update
	// request gets out (2s).
	Grace time.Duration
	// Settle is how long the new container must keep running, or be healthy if it has a
	// health check, before the old one is removed (15s); Limit bounds that wait (3m).
	Settle, Limit time.Duration
	// Poll is the gap between checks of the new container (1s).
	Poll time.Duration
}

func (o *SwapOptions) defaults() {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.After == nil {
		o.After = time.After
	}
	if o.Grace == 0 {
		o.Grace = 2 * time.Second
	}
	if o.Settle == 0 {
		o.Settle = 15 * time.Second
	}
	if o.Limit == 0 {
		o.Limit = 3 * time.Minute
	}
	if o.Poll == 0 {
		o.Poll = time.Second
	}
}

// ErrCurrent means the container already runs the image its reference names now.
var ErrCurrent = errors.New("the container already runs the newest image")

// Swap is the helper's side: it replaces container id with one of the image its
// reference names now, with the same settings. If the new container doesn't come up, it
// is removed and the old one is put back.
func Swap(ctx context.Context, d Docker, id string, opts SwapOptions) error {
	opts.defaults()
	res, err := d.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	old := res.Container
	if old.Config == nil || old.HostConfig == nil {
		return errors.New("inspect container: incomplete settings")
	}
	if old.HostConfig.AutoRemove {
		return ErrAutoRemove
	}
	if Pinned(old.Config.Image) {
		return ErrPinned
	}
	img, err := d.ImageInspect(ctx, old.Config.Image)
	if err != nil {
		return fmt.Errorf("inspect image %s: %w", old.Config.Image, err)
	}
	if img.ID == old.Image {
		return ErrCurrent
	}
	cfg, hc, nc := docker.RecreateSettings(old)
	// Drop what the old image set, so the new image's defaults apply.
	if oldImg, err := d.ImageInspect(ctx, old.Image); err == nil {
		docker.DropImageDefaults(cfg, oldImg.Config)
	}
	docker.KeepAnonymousVolumes(old, hc)
	mountDockerRoot(ctx, d, old, hc, opts.Log)

	name := strings.TrimPrefix(old.Name, "/")
	if err := clearAside(ctx, d, name+oldSuffix, opts.Log); err != nil {
		return err
	}
	if err := wait(ctx, opts.After(opts.Grace)); err != nil {
		return err
	}
	log := opts.Log.With("container", old.ID, "name", name, "image", old.Config.Image)
	log.Info("self-update: stopping the old container")
	if _, err := d.ContainerStop(ctx, old.ID, client.ContainerStopOptions{}); err != nil {
		restore(d, old, "", log, opts.After)
		return fmt.Errorf("stop: %w", err)
	}
	if _, err := d.ContainerRename(ctx, old.ID, client.ContainerRenameOptions{NewName: name + oldSuffix}); err != nil {
		restore(d, old, "", log, opts.After)
		return fmt.Errorf("rename: %w", err)
	}
	created, err := d.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: cfg, HostConfig: hc, NetworkingConfig: nc})
	if err != nil {
		restore(d, old, "", log, opts.After)
		return fmt.Errorf("create: %w", err)
	}
	if _, err := d.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		restore(d, old, created.ID, log, opts.After)
		return fmt.Errorf("start: %w", err)
	}
	if err := settle(ctx, d, created.ID, opts); err != nil {
		restore(d, old, created.ID, log, opts.After)
		return err
	}
	if _, err := d.ContainerRemove(ctx, old.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
		// The new container runs; the old one only has to be removed by hand.
		log.Warn("self-update: remove the old container", "err", err)
	}
	log.Info("self-update: done", "new", created.ID)
	return nil
}

// mountDockerRoot adds a read-only bind of the daemon's root dir (/var/lib/docker) where
// the container looks for it, for containers created before the install commands mounted
// it. Anything already mounted there, or above it, is left alone; so is a container that
// turned the disk size off with an empty NUDGE_DOCKER_ROOT.
func mountDockerRoot(ctx context.Context, d Docker, old container.InspectResponse, hc *container.HostConfig, log *slog.Logger) {
	target := dockerRootTarget(old.Config)
	if target == "" || !path.IsAbs(target) {
		return
	}
	target = path.Clean(target)
	for _, m := range old.Mounts {
		if dst := path.Clean(m.Destination); dst == target || dst == "/" || strings.HasPrefix(target, dst+"/") {
			return
		}
	}
	info, err := d.Info(ctx, client.InfoOptions{})
	if err != nil {
		log.Warn("self-update: Docker's root dir is unknown, not mounting it", "err", err)
		return
	}
	if info.Info.DockerRootDir == "" {
		log.Warn("self-update: Docker's root dir is unknown, not mounting it")
		return
	}
	hc.Binds = append(hc.Binds, info.Info.DockerRootDir+":"+target+":ro")
	log.Info("self-update: mounting Docker's root dir", "source", info.Info.DockerRootDir, "target", target)
}

// dockerRootTarget is where the container's nudge looks for Docker's root dir: its
// -docker-root flag, else NUDGE_DOCKER_ROOT, else the default. As for flag and Docker,
// the last of repeated ones counts.
func dockerRootTarget(cfg *container.Config) string {
	args := slices.Concat(cfg.Entrypoint, cfg.Cmd)
	flagSet, target := false, ""
	for i, a := range args {
		name, value, ok := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
		if name != "docker-root" || a == name {
			continue
		}
		switch {
		case ok:
			flagSet, target = true, value
		case i+1 < len(args):
			flagSet, target = true, args[i+1]
		}
	}
	if flagSet {
		return target
	}
	target = config.DefaultDockerRoot
	for _, e := range cfg.Env {
		if v, ok := strings.CutPrefix(e, "NUDGE_DOCKER_ROOT="); ok {
			target = v
		}
	}
	return target
}

// settle waits until container id has run (and been healthy, if it has a health check)
// for opts.Settle, or fails when it stops, turns unhealthy or opts.Limit passes.
func settle(ctx context.Context, d Docker, id string, opts SwapOptions) error {
	deadline := opts.After(opts.Limit)
	var steady time.Duration
	for {
		res, err := d.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return fmt.Errorf("inspect new container: %w", err)
		}
		st := res.Container.State
		switch {
		case st == nil || st.Status == container.StateExited || st.Status == container.StateDead:
			return errors.New("the new container stopped")
		case st.Health != nil && st.Health.Status == container.Unhealthy:
			return errors.New("the new container is unhealthy")
		case st.Running && !st.Restarting && (st.Health == nil || st.Health.Status == container.Healthy):
			steady += opts.Poll
		default:
			steady = 0
		}
		if steady >= opts.Settle {
			return nil
		}
		select {
		case <-opts.After(opts.Poll):
		case <-deadline:
			return errors.New("the new container didn't come up in time")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// restore removes the new container, if any, gives the old one its name back and starts
// it. It runs on its own timeout, so it still happens when the swap's ran out.
func restore(d Docker, old container.InspectResponse, newID string, log *slog.Logger, after func(time.Duration) <-chan time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if newID != "" {
		if _, err := d.ContainerRemove(ctx, newID, client.ContainerRemoveOptions{Force: true}); err != nil {
			log.Error("self-update: remove the new container", "container", newID, "err", err)
		}
	}
	if _, err := d.ContainerRename(ctx, old.ID, client.ContainerRenameOptions{NewName: strings.TrimPrefix(old.Name, "/")}); err != nil && !docker.InvalidArgument(err) {
		log.Error("self-update: rename the old container back", "err", err)
	}
	// The new container may still hold a port for a moment: try a few times.
	for try := 1; ; try++ {
		_, err := d.ContainerStart(ctx, old.ID, client.ContainerStartOptions{})
		if err == nil {
			log.Warn("self-update: failed; the old container runs again")
			return
		}
		if try == restoreTries || wait(ctx, after(2*time.Second)) != nil {
			log.Error("self-update: start the old container", "err", err)
			return
		}
	}
}

// restoreTries is how often restore tries to start the old container.
const restoreTries = 5

// clearAside removes a stopped container left under the aside name by an earlier update
// whose last step failed; a running one refuses the update, as the swap would need the name.
func clearAside(ctx context.Context, d Docker, aside string, log *slog.Logger) error {
	res, err := d.ContainerInspect(ctx, aside, client.ContainerInspectOptions{})
	if docker.NotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", aside, err)
	}
	if st := res.Container.State; st != nil && (st.Running || st.Restarting) {
		return fmt.Errorf("a container named %s runs (left by an earlier update?): remove it first", aside)
	}
	if _, err := d.ContainerRemove(ctx, res.Container.ID, client.ContainerRemoveOptions{}); err != nil {
		return fmt.Errorf("remove %s left by an earlier update: %w", aside, err)
	}
	log.Info("self-update: removed the container an earlier update left", "name", aside)
	return nil
}

func wait(ctx context.Context, c <-chan time.Time) error {
	select {
	case <-c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
