package stacks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// maxConfigOutput bounds what `compose config` may print for the bind check; a bigger
// project is deployed unchecked.
const maxConfigOutput = 8 << 20

// Mount is one mount of the container nudge runs in.
type Mount struct {
	// Type is "bind", "volume", "tmpfs", ...
	Type string
	// Name is a volume's name.
	Name string
	// Source is the host path of a bind (of a volume, its directory on the host).
	Source      string
	Destination string
}

// MountsFunc returns the mounts of the container nudge runs in; in is false when nudge
// doesn't run in a container the daemon knows, and then every bind can be mounted.
type MountsFunc func(ctx context.Context) (mounts []Mount, in bool, err error)

// BindError means a stack binds a path in its directory that the daemon would take from
// a different place on the host: nudge has the stacks directory from a volume, from
// another host path, or from its own filesystem.
type BindError struct {
	// Source is the bind source as the stack names it, relative to its directory.
	Source string
	// Path is the absolute path in nudge's container, which the daemon reads on the host.
	Path string
	// Mount is the mount of nudge's container that holds Path, nil if none does.
	Mount *Mount
}

func (e *BindError) Error() string {
	var from string
	switch {
	case e.Mount == nil:
		from = "from its own filesystem, not from a mount"
	case e.Mount.Type == "volume":
		from = fmt.Sprintf("from the volume %s mounted at %s", e.Mount.Name, e.Mount.Destination)
	case e.Mount.Type == "bind":
		from = fmt.Sprintf("from the host's %s mounted at %s", e.Mount.Source, e.Mount.Destination)
	default:
		from = fmt.Sprintf("from a %s mount at %s", e.Mount.Type, e.Mount.Destination)
	}
	return fmt.Sprintf("the bind mount %s can't be deployed: the daemon takes %s from the host, but nudge has it %s; "+
		"mount the stacks directory at the same path on the host and set NUDGE_STACKS_DIR to it", e.Source, e.Path, from)
}

// checkBinds returns a *BindError if stack name binds a path in its directory that the
// daemon would take from a different place on the host. Without l.mounts, or if compose
// can't read the project (up reports why), it passes.
func (l *Local) checkBinds(ctx context.Context, name string) error {
	if l.mounts == nil {
		return nil
	}
	out := &cappedBuffer{max: maxConfigOutput}
	code, err := l.exec(ctx, l.command(name, []string{"config", "--format", "json"}, out, io.Discard))
	if err != nil {
		return fmt.Errorf("compose config %s: %w", name, err)
	}
	if code != 0 || out.over {
		return nil
	}
	dir := l.dir.Path(name)
	sources, err := bindSources(out.Bytes(), dir)
	if err != nil {
		return fmt.Errorf("compose config %s: %w", name, err)
	}
	if len(sources) == 0 {
		return nil
	}
	mounts, in, err := l.mounts(ctx)
	if err != nil {
		return fmt.Errorf("bind check %s: %w", name, err)
	}
	if !in {
		return nil
	}
	for _, src := range sources {
		m := covering(mounts, src)
		if m != nil && m.Type == "bind" && filepath.Clean(m.Source) == filepath.Clean(m.Destination) {
			continue
		}
		rel, err := filepath.Rel(dir, src)
		if err != nil {
			rel = src
		} else {
			rel = "./" + filepath.ToSlash(rel)
		}
		return &BindError{Source: strings.TrimSuffix(rel, "/."), Path: src, Mount: m}
	}
	return nil
}

// bindSources returns the bind sources of the project compose config printed as JSON
// that are under dir, sorted: service volumes of type bind and file secrets and configs
// (compose bind-mounts those too).
func bindSources(config []byte, dir string) ([]string, error) {
	var p struct {
		Services map[string]struct {
			Volumes []struct {
				Type   string `json:"type"`
				Source string `json:"source"`
			} `json:"volumes"`
		} `json:"services"`
		Secrets map[string]struct {
			File string `json:"file"`
		} `json:"secrets"`
		Configs map[string]struct {
			File string `json:"file"`
		} `json:"configs"`
	}
	if err := json.Unmarshal(config, &p); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var all []string
	for _, s := range p.Services {
		for _, v := range s.Volumes {
			if v.Type == "bind" {
				all = append(all, v.Source)
			}
		}
	}
	for _, s := range p.Secrets {
		all = append(all, s.File)
	}
	for _, c := range p.Configs {
		all = append(all, c.File)
	}
	sources := []string{}
	for _, src := range all {
		if src != "" && filepath.IsAbs(src) && within(dir, filepath.Clean(src)) {
			sources = append(sources, filepath.Clean(src))
		}
	}
	slices.Sort(sources)
	return slices.Compact(sources), nil
}

// covering returns the mount that holds path: the one with the longest destination
// path is in, nil if none.
func covering(mounts []Mount, path string) *Mount {
	var best *Mount
	for i := range mounts {
		m := &mounts[i]
		if m.Destination == "" || !within(filepath.Clean(m.Destination), path) {
			continue
		}
		if best == nil || len(filepath.Clean(m.Destination)) > len(filepath.Clean(best.Destination)) {
			best = m
		}
	}
	return best
}

// within reports whether path is dir or below it; both are clean and absolute.
func within(dir, path string) bool {
	if dir == "/" || path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+"/")
}

// cappedBuffer keeps up to max bytes and notes whether more were written.
type cappedBuffer struct {
	bytes.Buffer
	max  int
	over bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.over || b.Len()+len(p) > b.max {
		b.over = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
