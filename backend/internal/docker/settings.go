package docker

import (
	"maps"
	"path"
	"slices"
	"strings"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

// DropImageDefaults removes from cfg what the container's image sets (its environment,
// command, labels, volumes, ports, …), as Docker copies them into every container. A
// recreate then takes them from the image it runs, which may be newer, and the spec
// shows only the container's own settings.
func DropImageDefaults(cfg *container.Config, img *dockerspec.DockerOCIImageConfig) {
	if img == nil {
		return
	}
	cfg.Env = slices.DeleteFunc(slices.Clone(cfg.Env), func(e string) bool { return slices.Contains(img.Env, e) })
	if slices.Equal(cfg.Cmd, img.Cmd) {
		cfg.Cmd = nil
	}
	if slices.Equal(cfg.Entrypoint, img.Entrypoint) {
		cfg.Entrypoint = nil
	}
	if cfg.WorkingDir == img.WorkingDir {
		cfg.WorkingDir = ""
	}
	if cfg.User == img.User {
		cfg.User = ""
	}
	if cfg.StopSignal == img.StopSignal {
		cfg.StopSignal = ""
	}
	if hc, ihc := cfg.Healthcheck, img.Healthcheck; hc != nil && ihc != nil && slices.Equal(hc.Test, ihc.Test) &&
		hc.Interval == ihc.Interval && hc.Timeout == ihc.Timeout && hc.Retries == ihc.Retries {
		cfg.Healthcheck = nil
	}
	cfg.Labels = maps.Clone(cfg.Labels)
	maps.DeleteFunc(cfg.Labels, func(k, v string) bool { iv, ok := img.Labels[k]; return ok && iv == v })
	cfg.Volumes = maps.Clone(cfg.Volumes)
	maps.DeleteFunc(cfg.Volumes, func(k string, _ struct{}) bool { _, ok := img.Volumes[k]; return ok })
	cfg.ExposedPorts = maps.Clone(cfg.ExposedPorts)
	maps.DeleteFunc(cfg.ExposedPorts, func(p network.Port, _ struct{}) bool { _, ok := img.ExposedPorts[p.String()]; return ok })
}

// RecreateSettings copies a container's settings for its replacement, leaving out what
// Docker generated for it: the hostname (its short ID) and the endpoints' addresses.
func RecreateSettings(old container.InspectResponse) (*container.Config, *container.HostConfig, *network.NetworkingConfig) {
	cfg, hc := *old.Config, *old.HostConfig
	if strings.HasPrefix(old.ID, cfg.Hostname) && cfg.Hostname != "" {
		cfg.Hostname = ""
	}
	hc.Mounts = slices.Clone(hc.Mounts)
	hc.Binds = slices.Clone(hc.Binds)
	cfg.ExposedPorts = maps.Clone(cfg.ExposedPorts)
	nc := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	if ns := old.NetworkSettings; ns != nil {
		for name, ep := range ns.Networks {
			if ep == nil {
				continue
			}
			nc.EndpointsConfig[name] = &network.EndpointSettings{
				IPAMConfig: ep.IPAMConfig, Links: ep.Links, Aliases: ep.Aliases,
				DriverOpts: ep.DriverOpts, GwPriority: ep.GwPriority,
			}
		}
	}
	return &cfg, &hc, nc
}

// KeepAnonymousVolumes mounts the old container's anonymous volumes (from an image's
// VOLUME, `-v /path` or a volume mount without a source) at the same paths, so their data
// survives a recreate.
func KeepAnonymousVolumes(old container.InspectResponse, hc *container.HostConfig) {
	anon := map[string]container.MountPoint{}
	for _, m := range old.Mounts {
		if m.Type == mount.TypeVolume {
			anon[path.Clean(m.Destination)] = m
		}
	}
	mounted := map[string]bool{}
	for i, m := range hc.Mounts {
		target := path.Clean(m.Target)
		if om, ok := anon[target]; ok && m.Type == mount.TypeVolume && m.Source == "" {
			hc.Mounts[i].Source = om.Name
		}
		mounted[target] = true
	}
	for _, b := range hc.Binds {
		parts := strings.Split(b, ":")
		if len(parts) >= 2 {
			mounted[path.Clean(parts[1])] = true
		}
	}
	for _, m := range old.Mounts {
		// Named volumes have a mount or bind of their own.
		if m.Type != mount.TypeVolume || mounted[path.Clean(m.Destination)] {
			continue
		}
		hc.Mounts = append(hc.Mounts, mount.Mount{Type: mount.TypeVolume, Source: m.Name, Target: m.Destination, ReadOnly: !m.RW})
	}
}
