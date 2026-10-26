package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/usage"
)

// volumeTimeout bounds a volume create or remove; a plugin driver may mount or unmount
// remote storage.
const volumeTimeout = time.Minute

// volumeResponse leaves out the driver options: a CIFS or NFS volume keeps its mount
// credentials there.
type volumeResponse struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Scope      string            `json:"scope"`
	CreatedAt  *time.Time        `json:"created_at"`
	Labels     map[string]string `json:"labels"`
	// Size is the last measured size in bytes; null when the volume wasn't measured yet
	// or its driver can't tell, and then SizeMeasuredAt says which.
	Size           *int64     `json:"size"`
	SizeMeasuredAt *time.Time `json:"size_measured_at"`
}

func (s *server) listVolumes(w http.ResponseWriter, r *http.Request) {
	s.dockerCall(w, r, "list volumes", func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.VolumeList(ctx, client.VolumeListOptions{})
		if err != nil {
			return nil, err
		}
		sizes := s.volumeSizes(ctx, envID(r))
		resp := make([]volumeResponse, len(res.Items))
		for i, v := range res.Items {
			resp[i] = withSize(toVolume(v), sizes)
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
		return withSize(toVolume(res.Volume), s.volumeSizes(ctx, envID(r))), nil
	})
}

// volumeSizes are the environment's last measured volume sizes. Best effort: without
// them the volumes are still listed.
func (s *server) volumeSizes(ctx context.Context, env int64) map[string]usage.Volume {
	if s.usage == nil {
		return nil
	}
	sizes, err := s.usage.Volumes(ctx, env)
	if err != nil {
		s.log.Warn("volume sizes", "env", env, "err", err)
		return nil
	}
	return sizes
}

func withSize(v volumeResponse, sizes map[string]usage.Volume) volumeResponse {
	if u, ok := sizes[v.Name]; ok {
		at := u.MeasuredAt.UTC()
		v.Size, v.SizeMeasuredAt = u.Size, &at
	}
	return v
}

// measureVolumes measures the environment's volume sizes now and answers once they're
// stored; 409 while a measurement of it runs. The measurement outlives the request (a
// proxy may cut a long one) and ends only on its own timeout or at shutdown.
func (s *server) measureVolumes(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil {
		s.writeError(w, http.StatusServiceUnavailable, "volume sizes aren't available")
		return
	}
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	stop := context.AfterFunc(s.done, cancel)
	defer stop()
	err := s.usage.Measure(ctx, env)
	switch {
	case errors.Is(err, usage.ErrRunning):
		s.writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, usage.ErrSave):
		s.internalError(w, "measure volumes", err)
	case err != nil:
		s.dockerError(w, r, env, "measure volumes", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func toVolume(v volume.Volume) volumeResponse {
	return volumeResponse{
		Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, Scope: v.Scope,
		CreatedAt: parseTime(v.CreatedAt), Labels: nonNilMap(v.Labels),
	}
}

// createVolumeRequest is a new volume; an empty name lets Docker pick one and an empty
// driver is "local". Options are passed to the driver and never returned or logged.
type createVolumeRequest struct {
	Name    string            `json:"name"`
	Driver  string            `json:"driver"`
	Options map[string]string `json:"options"`
	Labels  map[string]string `json:"labels"`
}

type pruneVolumesResponse struct {
	Deleted        []string `json:"deleted"`
	SpaceReclaimed uint64   `json:"space_reclaimed"`
}

// createVolume creates a volume and answers 201 with it. Docker answers an existing
// volume of the same name and driver instead of failing, like `docker volume create`.
func (s *server) createVolume(w http.ResponseWriter, r *http.Request) {
	var req createVolumeRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	auditTarget(r, req.Name)
	if msg := validCreate(req.Name, req.Driver, req.Options, req.Labels); msg != "" {
		s.writeError(w, http.StatusBadRequest, msg)
		return
	}
	resp, ok := s.runDocker(context.WithoutCancel(r.Context()), w, r, "volume create", "a volume with this name already exists with another driver", volumeTimeout, func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.VolumeCreate(ctx, client.VolumeCreateOptions{Name: req.Name, Driver: req.Driver, DriverOpts: req.Options, Labels: req.Labels})
		if err != nil {
			return nil, createError(err, "volume")
		}
		return toVolume(res.Volume), nil
	})
	if ok {
		s.writeJSON(w, http.StatusCreated, resp)
	}
}

// removeVolume removes a volume; ?force=true also removes it when its driver can't
// (Docker never removes a volume a container uses).
func (s *server) removeVolume(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "volume")
	if !ok {
		return
	}
	force, ok := s.queryBool(w, r, "force")
	if !ok {
		return
	}
	s.dockerAction(w, r, "volume", "the volume is in use by a container", volumeTimeout, func(ctx context.Context, c docker.Client) error {
		_, err := c.VolumeRemove(ctx, id, client.VolumeRemoveOptions{Force: force})
		return err
	})
}

// pruneVolumes removes the anonymous volumes no container uses, or with ?all=true the
// named ones too.
func (s *server) pruneVolumes(w http.ResponseWriter, r *http.Request) {
	all, ok := s.queryBool(w, r, "all")
	if !ok {
		return
	}
	resp, ok := s.runDocker(context.WithoutCancel(r.Context()), w, r, "volume prune", "a volume prune is already running", pruneTimeout, func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.VolumePrune(ctx, client.VolumePruneOptions{All: all})
		if err != nil {
			return nil, err
		}
		return pruneVolumesResponse{Deleted: nonNil(res.Report.VolumesDeleted), SpaceReclaimed: res.Report.SpaceReclaimed}, nil
	})
	if ok {
		s.writeJSON(w, http.StatusOK, resp)
	}
}

// validCreate checks the fields volumes and networks share and returns the problem, if
// any. Docker checks the rest (such as a name it doesn't accept).
func validCreate(name, driver string, options, labels map[string]string) string {
	switch {
	case name != "" && !objectID.MatchString(name):
		return "name may only contain letters, digits, '_', '.' and '-' and must start with a letter or digit"
	case driver != "" && !imageRef.MatchString(driver):
		return "invalid driver name"
	case hasEmptyKey(options):
		return "option names must not be empty"
	case hasEmptyKey(labels):
		return "label names must not be empty"
	}
	return ""
}

func hasEmptyKey(m map[string]string) bool {
	_, ok := m[""]
	return ok
}

// createError turns the daemon's refusals of a create into 400s. Its messages can quote
// driver options, which may hold credentials, so they are neither answered nor logged:
// only errors runDocker answers by kind (conflict, timeout, offline) are kept.
func createError(err error, what string) error {
	switch {
	case docker.NotFound(err):
		// A missing driver plugin.
		return &badRequestError{msg: "the Docker host has no such " + what + " driver"}
	case docker.InvalidArgument(err), docker.Forbidden(err):
		return &badRequestError{msg: "the Docker host refused the " + what + " settings"}
	case docker.Conflict(err), docker.Unreachable(err), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	return fmt.Errorf("%s create failed (daemon message withheld, it may quote driver options)", what)
}
