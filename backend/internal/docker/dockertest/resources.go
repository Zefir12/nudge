package dockertest

import (
	"context"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
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
