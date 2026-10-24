package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
)

const (
	// pullTimeout bounds an image pull; large images on slow links take long.
	pullTimeout = time.Hour
	// pruneTimeout bounds an image prune, which removes every unused image at once.
	pruneTimeout = 5 * time.Minute
	// removeImageTimeout bounds an image remove, which deletes its layers.
	removeImageTimeout = 2 * time.Minute
)

type imageSummaryResponse struct {
	ID        string            `json:"id"`
	Tags      []string          `json:"tags"`
	Digests   []string          `json:"digests"`
	CreatedAt *time.Time        `json:"created_at"`
	Size      int64             `json:"size"`
	Labels    map[string]string `json:"labels"`
	// Dangling images have no tag.
	Dangling bool `json:"dangling"`
}

type imageDetailResponse struct {
	ID           string              `json:"id"`
	Tags         []string            `json:"tags"`
	Digests      []string            `json:"digests"`
	CreatedAt    *time.Time          `json:"created_at"`
	Size         int64               `json:"size"`
	Author       string              `json:"author"`
	Comment      string              `json:"comment"`
	Architecture string              `json:"architecture"`
	Variant      string              `json:"variant"`
	OS           string              `json:"os"`
	Config       imageConfigResponse `json:"config"`
	Layers       []string            `json:"layers"`
}

type imageConfigResponse struct {
	User         string            `json:"user"`
	Env          []string          `json:"env"`
	Cmd          []string          `json:"cmd"`
	Entrypoint   []string          `json:"entrypoint"`
	WorkingDir   string            `json:"working_dir"`
	ExposedPorts []string          `json:"exposed_ports"`
	Volumes      []string          `json:"volumes"`
	Labels       map[string]string `json:"labels"`
}

func (s *server) listImages(w http.ResponseWriter, r *http.Request) {
	s.dockerCall(w, r, "list images", func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.ImageList(ctx, client.ImageListOptions{})
		if err != nil {
			return nil, err
		}
		resp := make([]imageSummaryResponse, len(res.Items))
		for i, img := range res.Items {
			resp[i] = toImageSummary(img)
		}
		return resp, nil
	})
}

func (s *server) getImage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, imageRef, "image")
	if !ok {
		return
	}
	s.dockerCall(w, r, "image", func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.ImageInspect(ctx, id)
		if err != nil {
			return nil, err
		}
		return toImageDetail(res.InspectResponse), nil
	})
}

// imageTags drops the "<none>:<none>" placeholder older daemons report for untagged images.
func imageTags(tags []string) []string {
	r := []string{}
	for _, t := range tags {
		if t != "<none>:<none>" {
			r = append(r, t)
		}
	}
	return r
}

func imageDigests(digests []string) []string {
	r := []string{}
	for _, d := range digests {
		if d != "<none>@<none>" {
			r = append(r, d)
		}
	}
	return r
}

func toImageSummary(img image.Summary) imageSummaryResponse {
	tags := imageTags(img.RepoTags)
	return imageSummaryResponse{
		ID: img.ID, Tags: tags, Digests: imageDigests(img.RepoDigests), CreatedAt: unixTime(img.Created),
		Size: img.Size, Labels: nonNilMap(img.Labels), Dangling: len(tags) == 0,
	}
}

func toImageDetail(img image.InspectResponse) imageDetailResponse {
	r := imageDetailResponse{
		ID: img.ID, Tags: imageTags(img.RepoTags), Digests: imageDigests(img.RepoDigests),
		CreatedAt: parseTime(img.Created), Size: img.Size, Author: img.Author, Comment: img.Comment,
		Architecture: img.Architecture, Variant: img.Variant, OS: img.Os,
		Layers: nonNil(img.RootFS.Layers),
		Config: imageConfigResponse{Env: []string{}, Cmd: []string{}, Entrypoint: []string{}, ExposedPorts: []string{}, Volumes: []string{}, Labels: map[string]string{}},
	}
	if cfg := img.Config; cfg != nil {
		r.Config = imageConfigResponse{
			User: cfg.User, Env: nonNil(cfg.Env), Cmd: nonNil(cfg.Cmd), Entrypoint: nonNil(cfg.Entrypoint),
			WorkingDir: cfg.WorkingDir, ExposedPorts: sortedKeys(cfg.ExposedPorts),
			Volumes: sortedKeys(cfg.Volumes), Labels: nonNilMap(cfg.Labels),
		}
	}
	return r
}

// pullEvent is one line of a pull's progress stream. The last line has done or error.
type pullEvent struct {
	// ID is the layer the status is about; empty for the image as a whole.
	ID      string `json:"id,omitempty"`
	Status  string `json:"status,omitempty"`
	Current int64  `json:"current,omitempty"`
	Total   int64  `json:"total,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

// pruneImagesResponse lists what a prune removed, as Docker reports it: Deleted has the
// IDs of images and their layers, Untagged the references removed.
type pruneImagesResponse struct {
	Deleted        []string `json:"deleted"`
	Untagged       []string `json:"untagged"`
	SpaceReclaimed uint64   `json:"space_reclaimed"`
}

// pullImage pulls {"image": "..."} with the stored credentials of its registry and
// streams the progress as JSON lines (application/x-ndjson). Errors before the pull
// starts are ordinary JSON errors; later ones end the stream with an error line. The pull
// stops if the client goes away, like Ctrl-C on docker pull.
func (s *server) pullImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image string `json:"image"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	ref := strings.TrimSpace(req.Image)
	if !validPullRef(ref) {
		s.writeError(w, http.StatusBadRequest, "image must be a reference such as nginx:1.27 or ghcr.io/org/app:tag")
		return
	}
	env, c, ok := s.environment(w, r)
	if !ok {
		return
	}
	opts := client.ImagePullOptions{}
	auth, found, err := s.registries.AuthFor(r.Context(), ref)
	if errors.Is(err, secretbox.ErrDecrypt) {
		s.log.Error("registry credentials", "image", ref, "err", err)
		s.writeError(w, http.StatusInternalServerError, "the stored registry password can't be decrypted (was secret.key replaced?): enter it again under Registries")
		return
	}
	if err != nil {
		s.internalError(w, "registry credentials", err)
		return
	}
	if found {
		opts.RegistryAuth = encodeRegistryAuth(registry.AuthConfig{
			Username: auth.Username, Password: auth.Password, ServerAddress: auth.Address,
		})
	}
	ctx, cancel := context.WithTimeout(r.Context(), pullTimeout)
	defer cancel()
	resp, err := c.ImagePull(ctx, ref, opts)
	if err != nil {
		s.pullError(w, r, env, err, auth.Password)
		return
	}
	defer func() { _ = resp.Close() }()
	s.envs.Seen(r.Context(), &env)
	s.log.Info("image pull", "env", env.ID, "image", ref, "registry_auth", found, "ip", clientIP(r))

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	send := func(ev pullEvent) bool {
		if err := enc.Encode(ev); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			if r.Context().Err() == nil {
				s.log.Warn("image pull", "env", env.ID, "image", ref, "err", err)
				send(pullEvent{Error: pullStreamError(err)})
			}
			return
		}
		if !send(toPullEvent(msg, auth.Password)) {
			return
		}
		if msg.Error != nil {
			s.log.Warn("image pull failed", "env", env.ID, "image", ref, "err", msg.Error.Message)
			return
		}
	}
	send(pullEvent{Done: true})
}

// digest matches the part after "@" in a reference pinned to a digest.
var digest = regexp.MustCompile(`^[a-z0-9]+:[a-f0-9]{32,}$`)

// validPullRef checks a reference loosely; the daemon parses it. An "@" is only allowed
// before a digest, so the registry host credentials are picked for (registries.HostOf)
// is the one the daemon pulls from.
func validPullRef(ref string) bool {
	if !imageRef.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, "/") {
		return false
	}
	name, d, pinned := strings.Cut(ref, "@")
	return !pinned || (digest.MatchString(d) && !strings.Contains(name, "@"))
}

// pullError answers a pull the daemon refused before streaming. Unlike other Docker
// errors the daemon's message is shown, since it is the only explanation of a failed
// pull (unknown tag, access denied); the registry password is masked in it.
func (s *server) pullError(w http.ResponseWriter, r *http.Request, env environments.Environment, err error, password string) {
	var code int
	switch {
	case r.Context().Err() != nil || errors.Is(err, context.DeadlineExceeded) || docker.Unreachable(err):
		s.dockerError(w, r, env, "image pull", err)
		return
	case docker.NotFound(err):
		code = http.StatusNotFound
	case docker.InvalidArgument(err):
		code = http.StatusBadRequest
	case docker.Unauthorized(err):
		// Not 401, which means the nudge session is gone.
		code = http.StatusForbidden
	default:
		code = http.StatusBadGateway
	}
	s.envs.Seen(r.Context(), &env)
	s.log.Warn("image pull", "env", env.ID, "err", err)
	s.writeError(w, code, "pull failed: "+maskSecret(strings.TrimPrefix(err.Error(), "Error response from daemon: "), password))
}

// encodeRegistryAuth is the X-Registry-Auth header value: the credentials as JSON in
// URL-safe base64.
func encodeRegistryAuth(a registry.AuthConfig) string {
	// Marshalling a struct of strings can't fail.
	b, _ := json.Marshal(a) // #nosec G117 -- the header hands the credentials to the daemon by design

	return base64.URLEncoding.EncodeToString(b)
}

func pullStreamError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "the pull did not finish in time"
	}
	return "lost the connection to the environment's Docker host"
}

func toPullEvent(msg jsonstream.Message, password string) pullEvent {
	ev := pullEvent{ID: msg.ID, Status: maskSecret(msg.Status, password)}
	if p := msg.Progress; p != nil {
		ev.Current, ev.Total = p.Current, p.Total
	}
	if msg.Error != nil {
		ev = pullEvent{Error: maskSecret(msg.Error.Message, password)}
	}
	return ev
}

// maskSecret hides secret in a message from the daemon, in case a registry echoes it.
func maskSecret(msg, secret string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, "********")
}

// removeImage removes an image (a tag removes only that tag while others remain).
// ?force=true also removes it when stopped containers use it or it has several tags.
func (s *server) removeImage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, imageRef, "image")
	if !ok {
		return
	}
	force, ok := s.queryBool(w, r, "force")
	if !ok {
		return
	}
	s.dockerAction(w, r, "image", "the image is in use by a container or has several tags: remove it with force=true (images of running containers can't be removed)", removeImageTimeout, func(ctx context.Context, c docker.Client) error {
		_, err := c.ImageRemove(ctx, id, client.ImageRemoveOptions{Force: force, PruneChildren: true})
		return err
	})
}

// pruneImages removes dangling images, or with ?all=true every image no container uses.
func (s *server) pruneImages(w http.ResponseWriter, r *http.Request) {
	all, ok := s.queryBool(w, r, "all")
	if !ok {
		return
	}
	filters := client.Filters{}
	if all {
		filters.Add("dangling", "false")
	} else {
		filters.Add("dangling", "true")
	}
	resp, ok := s.runDocker(context.WithoutCancel(r.Context()), w, r, "image prune", "an image prune is already running", pruneTimeout, func(ctx context.Context, c docker.Client) (any, error) {
		res, err := c.ImagePrune(ctx, client.ImagePruneOptions{Filters: filters})
		if err != nil {
			return nil, err
		}
		out := pruneImagesResponse{Deleted: []string{}, Untagged: []string{}, SpaceReclaimed: res.Report.SpaceReclaimed}
		for _, d := range res.Report.ImagesDeleted {
			if d.Deleted != "" {
				out.Deleted = append(out.Deleted, d.Deleted)
			}
			if d.Untagged != "" {
				out.Untagged = append(out.Untagged, d.Untagged)
			}
		}
		return out, nil
	})
	if ok {
		s.writeJSON(w, http.StatusOK, resp)
	}
}
