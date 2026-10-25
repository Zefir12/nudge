package stacks

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

// Files that say a process runs in a container (Docker, Podman).
var containerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

// containerPath finds a container ID in the root of a mount in /proc/self/mountinfo:
// the runtime mounts the container's /etc/hostname from .../containers/<id>/.
var containerPath = regexp.MustCompile(`containers/([0-9a-f]{64})/`)

// shortID is a hostname Docker set to the container's short ID.
var shortID = regexp.MustCompile(`^[0-9a-f]{12}$`)

// Self is the container this process runs in; a zero Self is none.
type Self struct {
	// ID is the container's ID, or its short ID.
	ID string
	// Hostname is this process's hostname, which the container's must match.
	Hostname string
}

// SelfID returns the container this process runs in: the ID from mountinfo, or else, if
// a runtime's marker file is there, the hostname when it looks like Docker's short ID
// (a hostname set with --hostname could name another container). Zero outside a
// container. readFile and hostname are os.ReadFile and os.Hostname outside tests.
func SelfID(readFile func(string) ([]byte, error), hostname func() (string, error)) Self {
	host := ""
	if h, err := hostname(); err == nil {
		host = strings.TrimSpace(h)
	}
	if info, err := readFile("/proc/self/mountinfo"); err == nil {
		if id := mountinfoID(string(info)); id != "" {
			return Self{ID: id, Hostname: host}
		}
	}
	if !shortID.MatchString(host) {
		return Self{}
	}
	for _, f := range containerMarkers {
		if _, err := readFile(f); err == nil {
			return Self{ID: host, Hostname: host}
		}
	}
	return Self{}
}

// Inspector is the Docker call ContainerMounts makes.
type Inspector interface {
	ContainerInspect(ctx context.Context, id string, opts client.ContainerInspectOptions) (client.ContainerInspectResult, error)
}

// ContainerMounts returns the MountsFunc of container self (SelfID), inspected with the
// client connect returns. A zero self, one the daemon doesn't know (nudge runs in a
// container of another daemon) or one with another hostname (not nudge's container) is
// no container.
func ContainerMounts(self Self, connect func() (Inspector, error)) MountsFunc {
	id := self.ID
	return func(ctx context.Context) ([]Mount, bool, error) {
		if id == "" {
			return nil, false, nil
		}
		c, err := connect()
		if err != nil {
			return nil, false, err
		}
		res, err := c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if docker.NotFound(err) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("inspect container %s: %w", id, err)
		}
		if cfg := res.Container.Config; cfg == nil || cfg.Hostname != self.Hostname {
			return nil, false, nil
		}
		mounts := make([]Mount, 0, len(res.Container.Mounts))
		for _, m := range res.Container.Mounts {
			mounts = append(mounts, Mount{Type: string(m.Type), Name: m.Name, Source: m.Source, Destination: m.Destination})
		}
		return mounts, true, nil
	}
}

// mountinfoID returns the container ID in the root of the /etc/hostname mount; only
// that line, as a Docker host's own mountinfo lists every container's shm mount.
func mountinfoID(info string) string {
	for line := range strings.Lines(info) {
		// ID, parent, major:minor, root, mount point, ...
		f := strings.Fields(line)
		if len(f) < 5 || f[4] != "/etc/hostname" {
			continue
		}
		if m := containerPath.FindStringSubmatch(f[3]); m != nil {
			return m[1]
		}
	}
	return ""
}
