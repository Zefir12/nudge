package api

import (
	"context"
	"net/http"
	"time"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
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
