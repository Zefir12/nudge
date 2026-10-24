package dockertest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"slices"
	"strings"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

// Resources is what a Fake lists and inspects. Inspect calls look the ID up in the
// matching map and answer not found when it is missing; ResourceErr fails every call.
type Resources struct {
	Containers       []container.Summary
	ContainerDetails map[string]container.InspectResponse
	Images           []image.Summary
	ImageDetails     map[string]image.InspectResponse
	Volumes          []volume.Volume
	Networks         []network.Summary
	NetworkDetails   map[string]network.Inspect
	ResourceErr      error
	// ActionErr fails the calls that change a container, image, volume or network (after
	// the not found check).
	ActionErr error
	// FailAction, if set, is asked before each action is recorded; an error fails that
	// call only (such as the start of a new container: Name "start", ID "new-1").
	FailAction func(a Action) error
	// PullMessages are the progress messages ImagePull streams; PullErr fails it before
	// any.
	PullMessages []jsonstream.Message
	PullErr      error
	// Logs is the raw stream ContainerLogs answers for each container ID (multiplexed,
	// unless the container has a TTY); a missing ID is not found. A followed stream then
	// stays open until its context is done. LogsErr fails every ContainerLogs call.
	Logs    map[string][]byte
	LogsErr error
	// LogsCancelled, if set, receives the container ID when a followed stream ends
	// because its context was cancelled (buffer it: the send doesn't wait).
	LogsCancelled chan string
	// PruneReport is what ImagePrune answers.
	PruneReport image.PruneReport
	// VolumePruneReport and NetworkPruneReport are what VolumePrune and NetworkPrune
	// answer.
	VolumePruneReport  volume.PruneReport
	NetworkPruneReport network.PruneReport
}

// Action is a recorded call that changes a container, image, volume or network: its name
// ("create", "rename", "start", "stop", "restart", "kill", "remove", "image-pull", "image-remove",
// "image-prune", "volume-create", "volume-remove", "volume-prune", "network-create",
// "network-remove", "network-prune"), the object and the options passed.
type Action struct {
	Name    string
	ID      string
	Options any
}

// ContainerList answers with Containers, filtered by the "status" filter like Docker.
func (f *Fake) ContainerList(ctx context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return client.ContainerListResult{}, err
	}
	var items []container.Summary
	for _, c := range f.Containers {
		status := opts.Filters["status"]
		if len(status) > 0 && !status[string(c.State)] {
			continue
		}
		if !opts.All && len(status) == 0 && c.State != container.StateRunning {
			continue
		}
		items = append(items, c)
	}
	return client.ContainerListResult{Items: items}, nil
}

// ContainerInspect answers with ContainerDetails[id].
func (f *Fake) ContainerInspect(ctx context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	c, err := lookup(ctx, f, f.ContainerDetails, id)
	return client.ContainerInspectResult{Container: c}, err
}

// ContainerStart records the action; the container must be in ContainerDetails.
func (f *Fake) ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, f.action(ctx, "start", id, opts)
}

// ContainerStop records the action; the container must be in ContainerDetails.
func (f *Fake) ContainerStop(ctx context.Context, id string, opts client.ContainerStopOptions) (client.ContainerStopResult, error) {
	return client.ContainerStopResult{}, f.action(ctx, "stop", id, opts)
}

// ContainerRestart records the action; the container must be in ContainerDetails.
func (f *Fake) ContainerRestart(ctx context.Context, id string, opts client.ContainerRestartOptions) (client.ContainerRestartResult, error) {
	return client.ContainerRestartResult{}, f.action(ctx, "restart", id, opts)
}

// ContainerKill records the action; the container must be in ContainerDetails.
func (f *Fake) ContainerKill(ctx context.Context, id string, opts client.ContainerKillOptions) (client.ContainerKillResult, error) {
	return client.ContainerKillResult{}, f.action(ctx, "kill", id, opts)
}

// ContainerRemove records the action; the container must be in ContainerDetails.
func (f *Fake) ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	return client.ContainerRemoveResult{}, f.action(ctx, "remove", id, opts)
}

// ContainerLogs records the options and streams Logs[id].
func (f *Fake) ContainerLogs(ctx context.Context, id string, opts client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return nil, err
	}
	if f.LogsErr != nil {
		return nil, f.LogsErr
	}
	data, ok := f.Logs[id]
	if !ok {
		return nil, cerrdefs.ErrNotFound
	}
	f.mu.Lock()
	f.logOptions = append(f.logOptions, opts)
	f.mu.Unlock()
	return &logStream{ctx: ctx, r: bytes.NewReader(data), follow: opts.Follow, id: id, cancelled: f.LogsCancelled}, nil
}

// LogOptions returns the options of every ContainerLogs call so far.
func (f *Fake) LogOptions() []client.ContainerLogsOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.logOptions)
}

// logStream reads its data, then, when following, blocks like a daemon waiting for
// more output until ctx is done.
type logStream struct {
	ctx       context.Context
	r         io.Reader
	follow    bool
	id        string
	cancelled chan string
	once      sync.Once
}

func (l *logStream) Read(p []byte) (int, error) {
	if err := l.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := l.r.Read(p)
	if n > 0 || !errors.Is(err, io.EOF) || !l.follow {
		return n, err
	}
	<-l.ctx.Done()
	l.once.Do(func() {
		if l.cancelled != nil {
			select {
			case l.cancelled <- l.id:
			default:
			}
		}
	})
	return 0, l.ctx.Err()
}

func (l *logStream) Close() error { return nil }

// Actions returns the recorded container actions, oldest first.
func (f *Fake) Actions() []Action {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Action(nil), f.actions...)
}

// ContainerCreate records the action (the ID is the requested name) and answers with the
// ID "new-<n>", which later container actions accept.
func (f *Fake) ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return client.ContainerCreateResult{}, err
	}
	if err := f.record("create", opts.Name, opts); err != nil {
		return client.ContainerCreateResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, fmt.Sprintf("new-%d", len(f.created)+1))
	return client.ContainerCreateResult{ID: f.created[len(f.created)-1]}, nil
}

// ContainerRename records the action; the container must be in ContainerDetails or
// created by ContainerCreate.
func (f *Fake) ContainerRename(ctx context.Context, id string, opts client.ContainerRenameOptions) (client.ContainerRenameResult, error) {
	return client.ContainerRenameResult{}, f.action(ctx, "rename", id, opts)
}

func (f *Fake) action(ctx context.Context, name, id string, opts any) error {
	if _, err := lookup(ctx, f, f.ContainerDetails, id); err != nil {
		f.mu.Lock()
		created := slices.Contains(f.created, id)
		f.mu.Unlock()
		if !created || !cerrdefs.IsNotFound(err) {
			return err
		}
	}
	return f.record(name, id, opts)
}

func (f *Fake) record(name, id string, opts any) error {
	if f.ActionErr != nil {
		return f.ActionErr
	}
	a := Action{Name: name, ID: id, Options: opts}
	if f.FailAction != nil {
		if err := f.FailAction(a); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, a)
	return nil
}

// ImageList answers with Images.
func (f *Fake) ImageList(ctx context.Context, _ client.ImageListOptions) (client.ImageListResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return client.ImageListResult{}, err
	}
	return client.ImageListResult{Items: f.Images}, nil
}

// ImageInspect answers with ImageDetails[id].
func (f *Fake) ImageInspect(ctx context.Context, id string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	img, err := lookup(ctx, f, f.ImageDetails, id)
	return client.ImageInspectResult{InspectResponse: img}, err
}

// ImagePull records the action and streams PullMessages.
func (f *Fake) ImagePull(ctx context.Context, ref string, opts client.ImagePullOptions) (client.ImagePullResponse, error) {
	if err := f.resourceErr(ctx); err != nil {
		return nil, err
	}
	if f.PullErr != nil {
		return nil, f.PullErr
	}
	if err := f.record("image-pull", ref, opts); err != nil {
		return nil, err
	}
	return &pullResponse{ReadCloser: io.NopCloser(strings.NewReader("")), msgs: f.PullMessages}, nil
}

type pullResponse struct {
	io.ReadCloser
	msgs []jsonstream.Message
}

func (p *pullResponse) JSONMessages(ctx context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(yield func(jsonstream.Message, error) bool) {
		for _, m := range p.msgs {
			if err := ctx.Err(); err != nil {
				yield(jsonstream.Message{}, err)
				return
			}
			if !yield(m, nil) {
				return
			}
		}
	}
}

func (p *pullResponse) Wait(ctx context.Context) error {
	for _, err := range p.JSONMessages(ctx) {
		if err != nil {
			return err
		}
	}
	return nil
}

// ImageRemove records the action; the image must be in ImageDetails.
func (f *Fake) ImageRemove(ctx context.Context, id string, opts client.ImageRemoveOptions) (client.ImageRemoveResult, error) {
	if _, err := lookup(ctx, f, f.ImageDetails, id); err != nil {
		return client.ImageRemoveResult{}, err
	}
	if err := f.record("image-remove", id, opts); err != nil {
		return client.ImageRemoveResult{}, err
	}
	return client.ImageRemoveResult{Items: []image.DeleteResponse{{Deleted: id}}}, nil
}

// ImagePrune records the action and answers with PruneReport.
func (f *Fake) ImagePrune(ctx context.Context, opts client.ImagePruneOptions) (client.ImagePruneResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return client.ImagePruneResult{}, err
	}
	if err := f.record("image-prune", "", opts); err != nil {
		return client.ImagePruneResult{}, err
	}
	return client.ImagePruneResult{Report: f.PruneReport}, nil
}

// VolumeList answers with Volumes.
func (f *Fake) VolumeList(ctx context.Context, _ client.VolumeListOptions) (client.VolumeListResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return client.VolumeListResult{}, err
	}
	return client.VolumeListResult{Items: f.Volumes}, nil
}

// VolumeInspect answers with the volume in Volumes named id.
func (f *Fake) VolumeInspect(ctx context.Context, id string, _ client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return client.VolumeInspectResult{}, err
	}
	for _, v := range f.Volumes {
		if v.Name == id {
			return client.VolumeInspectResult{Volume: v}, nil
		}
	}
	return client.VolumeInspectResult{}, cerrdefs.ErrNotFound
}

// VolumeCreate records the action (the ID is the requested name) and answers with the
// new volume.
func (f *Fake) VolumeCreate(ctx context.Context, opts client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return client.VolumeCreateResult{}, err
	}
	if err := f.record("volume-create", opts.Name, opts); err != nil {
		return client.VolumeCreateResult{}, err
	}
	v := volume.Volume{Name: opts.Name, Driver: opts.Driver, Labels: opts.Labels, Options: opts.DriverOpts, Scope: "local"}
	if v.Name == "" {
		v.Name = "generated"
	}
	if v.Driver == "" {
		v.Driver = "local"
	}
	return client.VolumeCreateResult{Volume: v}, nil
}

// VolumeRemove records the action; the volume must be in Volumes.
func (f *Fake) VolumeRemove(ctx context.Context, id string, opts client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	if _, err := f.VolumeInspect(ctx, id, client.VolumeInspectOptions{}); err != nil {
		return client.VolumeRemoveResult{}, err
	}
	return client.VolumeRemoveResult{}, f.record("volume-remove", id, opts)
}

// VolumePrune records the action and answers with VolumePruneReport.
func (f *Fake) VolumePrune(ctx context.Context, opts client.VolumePruneOptions) (client.VolumePruneResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return client.VolumePruneResult{}, err
	}
	if err := f.record("volume-prune", "", opts); err != nil {
		return client.VolumePruneResult{}, err
	}
	return client.VolumePruneResult{Report: f.VolumePruneReport}, nil
}

// NetworkList answers with Networks.
func (f *Fake) NetworkList(ctx context.Context, _ client.NetworkListOptions) (client.NetworkListResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return client.NetworkListResult{}, err
	}
	return client.NetworkListResult{Items: f.Networks}, nil
}

// NetworkInspect answers with NetworkDetails[id].
func (f *Fake) NetworkInspect(ctx context.Context, id string, _ client.NetworkInspectOptions) (client.NetworkInspectResult, error) {
	n, err := lookup(ctx, f, f.NetworkDetails, id)
	return client.NetworkInspectResult{Network: n}, err
}

// NetworkCreate records the action and answers with the ID "id-<name>".
func (f *Fake) NetworkCreate(ctx context.Context, name string, opts client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return client.NetworkCreateResult{}, err
	}
	if err := f.record("network-create", name, opts); err != nil {
		return client.NetworkCreateResult{}, err
	}
	return client.NetworkCreateResult{ID: "id-" + name}, nil
}

// NetworkRemove records the action; the network must be in NetworkDetails.
func (f *Fake) NetworkRemove(ctx context.Context, id string, opts client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
	if _, err := lookup(ctx, f, f.NetworkDetails, id); err != nil {
		return client.NetworkRemoveResult{}, err
	}
	return client.NetworkRemoveResult{}, f.record("network-remove", id, opts)
}

// NetworkPrune records the action and answers with NetworkPruneReport.
func (f *Fake) NetworkPrune(ctx context.Context, opts client.NetworkPruneOptions) (client.NetworkPruneResult, error) {
	if err := f.resourceErr(ctx); err != nil {
		return client.NetworkPruneResult{}, err
	}
	if err := f.record("network-prune", "", opts); err != nil {
		return client.NetworkPruneResult{}, err
	}
	return client.NetworkPruneResult{Report: f.NetworkPruneReport}, nil
}

func (f *Fake) resourceErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.ResourceErr
}

func lookup[T any](ctx context.Context, f *Fake, m map[string]T, id string) (T, error) {
	var zero T
	if err := f.resourceErr(ctx); err != nil {
		return zero, err
	}
	v, ok := m[id]
	if !ok {
		return zero, cerrdefs.ErrNotFound
	}
	return v, nil
}
