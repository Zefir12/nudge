package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
	"git.zefirlabs.net/zefir/nudge/backend/internal/updates"
)

// updateCheckTimeout bounds a check started from the API; registries are slow and an
// environment can run many images.
const updateCheckTimeout = 5 * time.Minute

type updateJSON struct {
	Image        string         `json:"image"`
	ImageID      string         `json:"image_id"`
	Status       updates.Status `json:"status"`
	LocalDigest  string         `json:"local_digest"`
	RemoteDigest string         `json:"remote_digest"`
	Error        string         `json:"error"`
	CheckedAt    time.Time      `json:"checked_at"`
}

func toUpdatesJSON(results []updates.Result) []updateJSON {
	out := make([]updateJSON, 0, len(results))
	for _, r := range results {
		out = append(out, updateJSON(r))
	}
	return out
}

// listUpdates answers the environment's last update check results.
func (s *server) listUpdates(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		s.writeError(w, http.StatusServiceUnavailable, "update checks aren't available")
		return
	}
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	results, err := s.updates.List(r.Context(), env.ID)
	if err != nil {
		s.internalError(w, "list updates", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toUpdatesJSON(results))
}

// checkUpdates checks the environment's images now and answers the results. The check
// isn't cancelled when the client goes away, so its results are saved, only at shutdown.
func (s *server) checkUpdates(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		s.writeError(w, http.StatusServiceUnavailable, "update checks aren't available")
		return
	}
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), updateCheckTimeout)
	defer cancel()
	// Shutdown doesn't wait for the check.
	defer context.AfterFunc(s.done, cancel)()
	results, err := s.updates.Check(ctx, env)
	if errors.Is(err, updates.ErrRunning) {
		s.writeError(w, http.StatusConflict, "an update check is already running; try again when it's done")
		return
	}
	if errors.Is(err, updates.ErrStore) {
		s.internalError(w, "check updates", err)
		return
	}
	if err != nil {
		s.dockerError(w, r, env, "check updates", err)
		return
	}
	s.envs.Seen(r.Context(), &env)
	s.writeJSON(w, http.StatusOK, toUpdatesJSON(results))
}

type updateContainerResponse struct {
	// Updated is false when the registry had nothing newer: the container was left alone.
	Updated         bool   `json:"updated"`
	ID              string `json:"id"`
	PreviousImageID string `json:"previous_image_id"`
	ImageID         string `json:"image_id"`
}

// updateContainer pulls the image a container was created from and, if that brought a
// newer one, recreates the container with it, keeping every setting (and restoring the
// old container if that fails). The image it ran before goes into the update history.
func (s *server) updateContainer(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, objectID, "container")
	if !ok {
		return
	}
	rp, ok := s.beginReplace(w, r, id)
	if !ok {
		return
	}
	defer rp.done()
	ref := rp.old.Config.Image
	if digest.MatchString(ref) || strings.Contains(ref, "@") || !validPullRef(ref) {
		s.writeError(w, http.StatusBadRequest, "the container runs an image by ID or digest, which has no newer version to pull")
		return
	}
	if !s.pullForReplace(w, r, rp, ref) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dockerTimeout)
	defer cancel()
	res, err := rp.c.ImageInspect(ctx, ref)
	if err != nil {
		s.dockerError(w, r, rp.env, "container update", fmt.Errorf("inspect pulled image: %w", err))
		return
	}
	pulled := res.InspectResponse
	if pulled.ID == rp.old.Image {
		s.envs.Seen(r.Context(), &rp.env)
		s.writeJSON(w, http.StatusOK, updateContainerResponse{ID: rp.old.ID, PreviousImageID: rp.old.Image, ImageID: pulled.ID})
		return
	}
	newID, ok := s.finishReplace(w, r, rp, nil)
	if !ok {
		return
	}
	s.recordUpdates(r, []updates.Change{{
		Environment: rp.env.ID, Kind: updates.KindContainer, Target: strings.TrimPrefix(rp.old.Name, "/"), Image: ref,
		OldImageID: rp.old.Image, OldDigest: updates.DigestFor(ref, rp.oldImage.RepoDigests),
		NewImageID: pulled.ID, NewDigest: updates.DigestFor(ref, pulled.RepoDigests),
	}})
	s.log.Info("container update", "env", rp.env.ID, "container", rp.old.ID, "new", newID, "image", pulled.ID, "ip", clientIP(r))
	s.writeJSON(w, http.StatusOK, updateContainerResponse{Updated: true, ID: newID, PreviousImageID: rp.old.Image, ImageID: pulled.ID})
}

// updateStackImages pulls a stack's images and deploys it (up with --pull always),
// streamed like up, then records each service whose image changed in the update history.
func (s *server) updateStackImages(w http.ResponseWriter, r *http.Request) {
	s.runStack(w, r, "update", func(ctx context.Context, run stackRun) (int, error) {
		lctx, cancel := context.WithTimeout(ctx, dockerTimeout)
		before, err := projectContainers(lctx, run.c, run.name)
		cancel()
		if err != nil {
			// The update can go on; only its history is lost.
			s.log.Warn("stack update: list containers", "env", run.env.ID, "stack", run.name, "err", err)
		}
		code, err := run.runner.Up(ctx, run.name, true, run.out)
		// Even a failed or cut-short run may have replaced some services.
		if before != nil {
			s.recordStackUpdate(ctx, r, run, before)
		}
		return code, err
	})
}

// recordStackUpdate records the services of a stack whose image changed since before.
func (s *server) recordStackUpdate(ctx context.Context, r *http.Request, run stackRun, before []container.Summary) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dockerTimeout)
	defer cancel()
	after, err := projectContainers(ctx, run.c, run.name)
	if err != nil {
		s.log.Warn("stack update: list containers", "env", run.env.ID, "stack", run.name, "err", err)
		return
	}
	changes := stackChanges(before, after)
	digests := map[string][]string{}
	repoDigests := func(id string) []string {
		if d, ok := digests[id]; ok {
			return d
		}
		res, err := run.c.ImageInspect(ctx, id)
		if err != nil && !docker.NotFound(err) {
			s.log.Warn("stack update: inspect image", "env", run.env.ID, "image", id, "err", err)
		}
		digests[id] = res.RepoDigests
		return res.RepoDigests
	}
	for i := range changes {
		c := &changes[i]
		c.Environment, c.Target = run.env.ID, run.name
		c.OldDigest = updates.DigestFor(c.Image, repoDigests(c.OldImageID))
		c.NewDigest = updates.DigestFor(c.Image, repoDigests(c.NewImageID))
	}
	s.recordUpdates(r, changes)
}

// stackChanges lists the services whose containers run another image after than
// before, by service name. Services that are new or gone weren't updated.
func stackChanges(before, after []container.Summary) []updates.Change {
	old := map[string]string{}
	for _, c := range before {
		if svc := c.Labels[stacks.ServiceLabel]; svc != "" && c.ImageID != "" {
			old[svc] = c.ImageID
		}
	}
	after = slices.Clone(after)
	slices.SortFunc(after, func(a, b container.Summary) int {
		return cmp.Compare(a.Labels[stacks.ServiceLabel], b.Labels[stacks.ServiceLabel])
	})
	var out []updates.Change
	seen := map[string]bool{}
	for _, c := range after {
		svc := c.Labels[stacks.ServiceLabel]
		if seen[svc] || old[svc] == "" || c.ImageID == "" || c.ImageID == old[svc] {
			continue
		}
		seen[svc] = true
		out = append(out, updates.Change{Kind: updates.KindStack, Service: svc, Image: c.Image, OldImageID: old[svc], NewImageID: c.ImageID})
	}
	return out
}

// recordUpdates saves changes made by r's user in the update history. The update has
// happened, so a failure is only logged.
func (s *server) recordUpdates(r *http.Request, changes []updates.Change) {
	if s.updates == nil || len(changes) == 0 {
		return
	}
	u, _ := userFrom(r.Context())
	for i := range changes {
		changes[i].User = u.Username
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
	defer cancel()
	if err := s.updates.Record(ctx, changes); err != nil {
		s.log.Error("record update", "err", err)
	}
}

type updateHistoryJSON struct {
	ID         int64        `json:"id"`
	Kind       updates.Kind `json:"kind"`
	Target     string       `json:"target"`
	Service    string       `json:"service"`
	Image      string       `json:"image"`
	OldImageID string       `json:"old_image_id"`
	OldDigest  string       `json:"old_digest"`
	NewImageID string       `json:"new_image_id"`
	NewDigest  string       `json:"new_digest"`
	User       string       `json:"user"`
	Time       time.Time    `json:"time"`
}

// listUpdateHistory answers the updates made in an environment, newest first;
// ?kind=container|stack&target=<name> narrows it to one container or stack.
func (s *server) listUpdateHistory(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		s.writeError(w, http.StatusServiceUnavailable, "update checks aren't available")
		return
	}
	q := r.URL.Query()
	kind := updates.Kind(q.Get("kind"))
	if kind != "" && kind != updates.KindContainer && kind != updates.KindStack {
		s.writeError(w, http.StatusBadRequest, "kind must be container or stack")
		return
	}
	env, _, ok := s.environment(w, r)
	if !ok {
		return
	}
	list, err := s.updates.History(r.Context(), env.ID, kind, q.Get("target"))
	if err != nil {
		s.internalError(w, "list update history", err)
		return
	}
	out := make([]updateHistoryJSON, 0, len(list))
	for _, c := range list {
		out = append(out, updateHistoryJSON{
			ID: c.ID, Kind: c.Kind, Target: c.Target, Service: c.Service, Image: c.Image,
			OldImageID: c.OldImageID, OldDigest: c.OldDigest, NewImageID: c.NewImageID, NewDigest: c.NewDigest,
			User: c.User, Time: c.Time,
		})
	}
	s.writeJSON(w, http.StatusOK, out)
}
