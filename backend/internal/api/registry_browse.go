package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/registryclient"
)

// RegistryBrowser browses and deletes images in a registry; registryclient.Client is
// the real one.
type RegistryBrowser interface {
	Repositories(ctx context.Context, reg registryclient.Registry) ([]string, error)
	Tags(ctx context.Context, reg registryclient.Registry, repo string) ([]string, error)
	Manifest(ctx context.Context, reg registryclient.Registry, repo, tag string) (registryclient.Manifest, error)
	SharedTags(ctx context.Context, reg registryclient.Registry, repo, tag, digest string) ([]string, bool, error)
	Delete(ctx context.Context, reg registryclient.Registry, repo, tag, digest string) error
}

// repositoriesPage is how many repositories one listing answers.
const repositoriesPage = 100

type repositoryItem struct {
	Name string `json:"name"`
}

type repositoriesResponse struct {
	Repositories []repositoryItem `json:"repositories"`
	// Next is the cursor of the following page, empty on the last one.
	Next string `json:"next,omitempty"`
}

type tagItem struct {
	Tag string `json:"tag"`
}

type platformResponse struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
	Digest       string `json:"digest"`
	Size         int64  `json:"size"`
}

type manifestResponse struct {
	Repository string             `json:"repository"`
	Tag        string             `json:"tag"`
	Digest     string             `json:"digest"`
	MediaType  string             `json:"media_type"`
	Size       int64              `json:"size,omitempty"`
	Created    *time.Time         `json:"created,omitempty"`
	Platforms  []platformResponse `json:"platforms"`
	// SharedTags are the repository's other tags on the same digest; a delete on a
	// generic registry removes them too.
	SharedTags []string `json:"shared_tags"`
	// SharedTagsComplete is false when not every tag could be checked.
	SharedTagsComplete bool `json:"shared_tags_complete"`
}

// browseTarget looks up the registry of the request's {id} with its credentials.
func (s *server) browseTarget(w http.ResponseWriter, r *http.Request) (registryclient.Registry, bool) {
	id, ok := s.registryID(w, r)
	if !ok {
		return registryclient.Registry{}, false
	}
	reg, password, err := s.registries.Credentials(r.Context(), id)
	if s.registryError(w, "registry credentials", err) {
		return registryclient.Registry{}, false
	}
	return registryclient.Registry{
		Address: reg.Address, Kind: reg.Kind, Owners: reg.Owners, PlainHTTP: reg.PlainHTTP,
		Username: reg.Username, Password: password,
	}, true
}

// repoParams reads and validates ?repository= and, if withTag, ?tag=.
func (s *server) repoParams(w http.ResponseWriter, r *http.Request, withTag bool) (repo, tag string, ok bool) {
	q := r.URL.Query()
	repo = q.Get("repository")
	if !registryclient.ValidRepository(repo) {
		s.writeError(w, http.StatusBadRequest, "repository must be a repository name such as team/app")
		return "", "", false
	}
	if withTag {
		tag = q.Get("tag")
		if !registryclient.ValidTag(tag) {
			s.writeError(w, http.StatusBadRequest, "tag must be a tag such as latest or v1.2")
			return "", "", false
		}
	}
	return repo, tag, true
}

// browseError answers a registry client error, without upstream detail, and reports
// whether there was one.
func (s *server) browseError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, registryclient.ErrNotFound):
		s.writeError(w, http.StatusNotFound, registryclient.ErrNotFound.Error())
	case errors.Is(err, registryclient.ErrNoCatalog):
		s.writeError(w, http.StatusConflict, registryclient.ErrNoCatalog.Error())
	case errors.Is(err, registryclient.ErrDeleteUnsupported):
		s.writeError(w, http.StatusConflict, registryclient.ErrDeleteUnsupported.Error())
	case errors.Is(err, registryclient.ErrDigestMismatch):
		s.writeError(w, http.StatusConflict, registryclient.ErrDigestMismatch.Error())
	case errors.Is(err, registryclient.ErrBadOwner):
		s.writeError(w, http.StatusConflict, registryclient.ErrBadOwner.Error())
	case errors.Is(err, registryclient.ErrUnauthorized):
		s.writeError(w, http.StatusBadGateway, registryclient.ErrUnauthorized.Error())
	case errors.Is(err, registryclient.ErrInsecureRealm):
		s.writeError(w, http.StatusBadGateway, registryclient.ErrInsecureRealm.Error())
	default:
		// The client's errors name the request (method and path) and status, never
		// credentials or response bodies.
		s.log.Warn("registry request failed", "err", err, "username", s.username(r))
		s.writeError(w, http.StatusBadGateway, registryclient.ErrUnreachable.Error())
	}
	return true
}

func (s *server) listRegistryRepositories(w http.ResponseWriter, r *http.Request) {
	reg, ok := s.browseTarget(w, r)
	if !ok {
		return
	}
	names, err := s.registryClient.Repositories(r.Context(), reg)
	if s.browseError(w, r, err) {
		return
	}
	q := strings.ToLower(r.URL.Query().Get("q"))
	cursor := r.URL.Query().Get("cursor")
	slices.Sort(names)
	resp := repositoriesResponse{Repositories: []repositoryItem{}}
	for _, name := range names {
		if name <= cursor || (q != "" && !strings.Contains(name, q)) {
			continue
		}
		if len(resp.Repositories) == repositoriesPage {
			resp.Next = resp.Repositories[len(resp.Repositories)-1].Name
			break
		}
		resp.Repositories = append(resp.Repositories, repositoryItem{Name: name})
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *server) listRegistryTags(w http.ResponseWriter, r *http.Request) {
	reg, ok := s.browseTarget(w, r)
	if !ok {
		return
	}
	repo, _, ok := s.repoParams(w, r, false)
	if !ok {
		return
	}
	tags, err := s.registryClient.Tags(r.Context(), reg, repo)
	if s.browseError(w, r, err) {
		return
	}
	slices.Sort(tags)
	resp := make([]tagItem, len(tags))
	for i, t := range tags {
		resp[i] = tagItem{Tag: t}
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *server) getRegistryManifest(w http.ResponseWriter, r *http.Request) {
	reg, ok := s.browseTarget(w, r)
	if !ok {
		return
	}
	repo, tag, ok := s.repoParams(w, r, true)
	if !ok {
		return
	}
	m, err := s.registryClient.Manifest(r.Context(), reg, repo, tag)
	if s.browseError(w, r, err) {
		return
	}
	shared, complete, err := s.registryClient.SharedTags(r.Context(), reg, repo, tag, m.Digest)
	if s.browseError(w, r, err) {
		return
	}
	resp := manifestResponse{
		Repository: repo, Tag: tag, Digest: m.Digest, MediaType: m.MediaType, Size: m.Size,
		Platforms: []platformResponse{}, SharedTags: shared, SharedTagsComplete: complete,
	}
	if resp.SharedTags == nil {
		resp.SharedTags = []string{}
	}
	if !m.Created.IsZero() {
		created := m.Created.UTC()
		resp.Created = &created
	}
	for _, p := range m.Platforms {
		resp.Platforms = append(resp.Platforms, platformResponse(p))
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *server) deleteRegistryTag(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	reg, ok := s.browseTarget(w, r)
	if !ok {
		return
	}
	repo, tag, ok := s.repoParams(w, r, true)
	if !ok {
		return
	}
	auditTarget(r, reg.Address+"/"+repo+":"+tag)
	digest := r.URL.Query().Get("digest")
	if !registryclient.ValidDigest(digest) {
		s.writeError(w, http.StatusBadRequest, "digest must be the tag's current digest (sha256:…)")
		return
	}
	if s.browseError(w, r, s.registryClient.Delete(r.Context(), reg, repo, tag, digest)) {
		return
	}
	s.log.Info("registry tag deleted", "registry", reg.Address, "repository", repo, "tag", tag, "digest", digest,
		"username", s.username(r), "ip", clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// requireAdmin answers 403 unless the request's user is an admin.
func (s *server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	u, ok := userFrom(r.Context())
	if !ok || !s.isAdmin(u) {
		s.writeError(w, http.StatusForbidden, "admins only")
		return false
	}
	return true
}
