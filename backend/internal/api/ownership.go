package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
)

const (
	// ownerLabel names the user who owns a container, volume or network, by their owner
	// key. Objects without it (created by an admin, by a stack or outside nudge) are the
	// admins'.
	ownerLabel = "net.zefirlabs.nudge.owner"
	// reservedLabels is the label namespace only nudge sets; requests can't.
	reservedLabels = "net.zefirlabs.nudge."
	// maxOwnerCache bounds an event stream's cache of container ownership.
	maxOwnerCache = 1024
)

// reservedKey reports whether a label key is in nudge's own namespace.
func reservedKey(k string) bool {
	return strings.HasPrefix(strings.ToLower(k), reservedLabels)
}

// reservedLabel reports whether labels has a key in nudge's own namespace.
func reservedLabel(labels map[string]string) bool {
	for k := range labels {
		if reservedKey(k) {
			return true
		}
	}
	return false
}

// scope is what an environment route shows its caller: everything to an admin, to a user
// only the objects labelled with their owner key. Someone else's object is answered as
// missing (404), so a user can't even learn that it exists.
type scope struct {
	all bool
	key string
}

// scopeOf returns the scope of the request's user.
func (s *server) scopeOf(ctx context.Context) (scope, error) {
	u, ok := userFrom(ctx)
	if !ok {
		return scope{}, &internalFailure{msg: "scope", err: errors.New("no user in the request")}
	}
	if isAdmin(u) {
		return scope{all: true}, nil
	}
	key, err := s.auth.OwnerKey(ctx, u.ID)
	if err != nil {
		return scope{}, &internalFailure{msg: "scope", err: err}
	}
	return scope{key: key}, nil
}

// owns reports whether labels make the object the scope's. For a container, use
// ownsContainer, which also checks its image.
func (sc scope) owns(labels map[string]string) bool {
	return sc.all || (sc.key != "" && labels[ownerLabel] == sc.key)
}

// filters narrows a Docker list to the scope's objects; the results are checked again.
func (sc scope) filters(f client.Filters) client.Filters {
	if sc.all {
		return f
	}
	out := make(client.Filters)
	for k, vs := range f {
		out[k] = maps.Clone(vs)
	}
	return out.Add("label", ownerLabel+"="+sc.key)
}

// ownsContainer reports whether the container with labels, run from image (an ID), is
// the scope's: its owner label is the key and the image sets no owner label. Docker merges
// an image's labels into its containers', so an image could otherwise hand a user a
// container an admin runs from it.
func (sc scope) ownsContainer(ctx context.Context, c docker.Client, labels map[string]string, image string) (bool, error) {
	if sc.all {
		return true, nil
	}
	if !sc.owns(labels) {
		return false, nil
	}
	res, err := c.ImageInspect(ctx, image)
	if docker.NotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect image: %w", err)
	}
	if res.Config != nil {
		if _, ok := res.Config.Labels[ownerLabel]; ok {
			return false, nil
		}
	}
	return true, nil
}

// ownedContainers keeps the listed containers the scope owns.
func (sc scope) ownedContainers(ctx context.Context, c docker.Client, items []container.Summary) ([]container.Summary, error) {
	if sc.all {
		return items, nil
	}
	images := map[string]bool{}
	out := []container.Summary{}
	for _, item := range items {
		if !sc.owns(item.Labels) {
			continue
		}
		ok, seen := images[item.ImageID]
		if !seen {
			var err error
			if ok, err = sc.ownsContainer(ctx, c, item.Labels, item.ImageID); err != nil {
				return nil, err
			}
			images[item.ImageID] = ok
		}
		if ok {
			out = append(out, item)
		}
	}
	return out, nil
}

// ownedContainer inspects container id (an ID or a name) and returns it if the request's
// user owns it, else docker.ErrNotFound. The returned ref is what later calls should name
// the container by: for a user its ID, so a name taken over meanwhile by someone else's
// container doesn't lead there; for an admin id as asked.
func (s *server) ownedContainer(ctx context.Context, c docker.Client, id string) (ct container.InspectResponse, ref string, err error) {
	sc, err := s.scopeOf(ctx)
	if err != nil {
		return container.InspectResponse{}, "", err
	}
	res, err := c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, "", err
	}
	ct = res.Container
	if sc.all {
		return ct, id, nil
	}
	var labels map[string]string
	if ct.Config != nil {
		labels = ct.Config.Labels
	}
	ok, err := sc.ownsContainer(ctx, c, labels, ct.Image)
	if err != nil {
		return container.InspectResponse{}, "", err
	}
	if !ok || ct.ID == "" {
		return container.InspectResponse{}, "", docker.ErrNotFound
	}
	return ct, ct.ID, nil
}

// ownedContainerID returns what to name container id by (see ownedContainer) if the
// request's user owns it, else docker.ErrNotFound. For an admin it is id itself, without
// asking the daemon.
func (s *server) ownedContainerID(ctx context.Context, c docker.Client, id string) (string, error) {
	sc, err := s.scopeOf(ctx)
	if err != nil {
		return "", err
	}
	if sc.all {
		return id, nil
	}
	_, ref, err := s.ownedContainer(ctx, c, id)
	return ref, err
}

// ownedVolume returns docker.ErrNotFound unless the request's user owns volume name.
// Volumes have no ID, so a remove after it names the volume again: a volume of the same
// name created by someone else in between would be removed (a tiny window, and never one
// a container uses).
func (s *server) ownedVolume(ctx context.Context, c docker.Client, name string) error {
	sc, err := s.scopeOf(ctx)
	if err != nil || sc.all {
		return err
	}
	res, err := c.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil {
		return err
	}
	if !sc.owns(res.Volume.Labels) {
		return docker.ErrNotFound
	}
	return nil
}

// ownedNetwork returns the ID of network id (an ID or a name) if the request's user owns
// it, else docker.ErrNotFound. For an admin it is id itself.
func (s *server) ownedNetwork(ctx context.Context, c docker.Client, id string) (string, error) {
	sc, err := s.scopeOf(ctx)
	if err != nil {
		return "", err
	}
	if sc.all {
		return id, nil
	}
	res, err := c.NetworkInspect(ctx, id, client.NetworkInspectOptions{})
	if err != nil {
		return "", err
	}
	if !sc.owns(res.Network.Labels) {
		return "", docker.ErrNotFound
	}
	return res.Network.ID, nil
}

// refuseReservedLabels answers 400 if a create request sets a label in nudge's own
// namespace, which only the server sets.
func (s *server) refuseReservedLabels(w http.ResponseWriter, labels map[string]string) bool {
	if reservedLabel(labels) {
		s.writeError(w, http.StatusBadRequest, "labels starting with "+reservedLabels+" are reserved for nudge")
		return true
	}
	return false
}

// ownerLabels returns labels with the request's user as the owner: a user's objects carry
// their owner key, an admin's none.
func (s *server) ownerLabels(ctx context.Context, labels map[string]string) (map[string]string, error) {
	sc, err := s.scopeOf(ctx)
	if err != nil || sc.all {
		return labels, err
	}
	out := maps.Clone(labels)
	if out == nil {
		out = map[string]string{}
	}
	out[ownerLabel] = sc.key
	return out, nil
}

// ownerCache remembers which containers an event stream's user owns, by container ID
// (a container's labels and image never change). It is bounded: when full it starts over.
type ownerCache struct {
	log    *slog.Logger
	sc     scope
	c      docker.Client
	owners map[string]bool
}

// event reports whether an event may go to the scope's user: only events of containers
// they own. Volume and network events carry no labels, so users get none of those, and
// neither do they get the event of a container that is gone before the stream first
// saw it (it can no longer be checked).
func (oc *ownerCache) event(ctx context.Context, typ, id string, attrs map[string]string) bool {
	if oc.sc.all {
		return true
	}
	if typ != "container" || !oc.sc.owns(attrs) {
		return false
	}
	if ok, seen := oc.owners[id]; seen {
		return ok
	}
	ctx, cancel := context.WithTimeout(ctx, dockerTimeout)
	defer cancel()
	ct, err := oc.c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		oc.log.Debug("events: owner check", "container", id, "err", err)
		return false
	}
	var labels map[string]string
	if ct.Container.Config != nil {
		labels = ct.Container.Config.Labels
	}
	ok, err := oc.sc.ownsContainer(ctx, oc.c, labels, ct.Container.Image)
	if err != nil {
		oc.log.Debug("events: owner check", "container", id, "err", err)
		return false
	}
	if len(oc.owners) >= maxOwnerCache {
		clear(oc.owners)
	}
	oc.owners[id] = ok
	return ok
}
