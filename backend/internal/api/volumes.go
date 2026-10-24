package api

import (
	"context"
	"net/http"
	"time"

	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

// volumeResponse leaves out the driver options: a CIFS or NFS volume keeps its mount
// credentials there.
type volumeResponse struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Scope      string            `json:"scope"`
	CreatedAt  *time.Time        `json:"created_at"`
	Labels     map[string]string `json:"labels"`
}

func (s *server) listVolumes(w http.ResponseWriter, r *http.Request) {
	s.dockerCall(w, r, "list volumes", func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.VolumeList(ctx, client.VolumeListOptions{})
		if err != nil {
			return nil, err
		}
		resp := make([]volumeResponse, len(res.Items))
		for i, v := range res.Items {
			resp[i] = toVolume(v)
		}
		sortBy(resp, func(v volumeResponse) string { return v.Name })
		return resp, nil
	})
}

func (s *server) getVolume(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "volume")
	if !ok {
		return
	}
	s.dockerCall(w, r, "volume", func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.VolumeInspect(ctx, id, client.VolumeInspectOptions{})
		if err != nil {
			return nil, err
		}
		return toVolume(res.Volume), nil
	})
}

func toVolume(v volume.Volume) volumeResponse {
	return volumeResponse{
		Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, Scope: v.Scope,
		CreatedAt: parseTime(v.CreatedAt), Labels: nonNilMap(v.Labels),
	}
}
