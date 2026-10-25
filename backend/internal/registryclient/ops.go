package registryclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
)

// Repositories lists the registry's repositories, sorted: the v2 catalog for generic
// registries, the owners' container packages for Forgejo.
func (c *Client) Repositories(ctx context.Context, reg Registry) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if reg.Kind == registries.Forgejo {
		return c.forgejoRepositories(ctx, reg)
	}
	return c.catalog(ctx, reg)
}

func (c *Client) catalog(ctx context.Context, reg Registry) ([]string, error) {
	root := base(reg)
	u := v2(reg, "_catalog")
	u.RawQuery = url.Values{"n": {strconv.Itoa(catalogPageSize)}}.Encode()
	seen := map[string]bool{}
	for page := 0; page < maxPages && u != nil; page++ {
		resp, err := c.do(ctx, reg, http.MethodGet, u, jsonAccept, "registry:catalog:*")
		if err != nil {
			return nil, err
		}
		if err := catalogStatus(resp); err != nil {
			return nil, err
		}
		var body struct {
			Repositories []string `json:"repositories"`
		}
		err = readJSON(resp, maxJSONBody, &body)
		next := nextLink(resp, u, root)
		drain(resp)
		if err != nil {
			return nil, err
		}
		for _, name := range body.Repositories {
			if ValidRepository(name) {
				seen[name] = true
			}
		}
		u = next
	}
	return sortedKeys(seen), nil
}

// catalogStatus checks a catalog answer: registries that don't offer a catalog answer
// 404 or 405, or refuse it to a token they issued (Docker Hub).
func catalogStatus(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		if strings.HasPrefix(resp.Request.Header.Get("Authorization"), "Bearer ") {
			drain(resp)
			return fmt.Errorf("%w: catalog refused to the token", ErrNoCatalog)
		}
	case http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
		drain(resp)
		return fmt.Errorf("%w: catalog answered %d", ErrNoCatalog, resp.StatusCode)
	}
	if slices.Contains(errorCodes(resp), "UNSUPPORTED") {
		drain(resp)
		return fmt.Errorf("%w: catalog unsupported", ErrNoCatalog)
	}
	err := statusError(resp)
	drain(resp)
	return err
}

// forgejoPackage is an entry of Forgejo's GET /api/v1/packages/{owner}: one per package
// version, so a multi-arch image appears once per tag and once per platform digest.
type forgejoPackage struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (c *Client) forgejoRepositories(ctx context.Context, reg Registry) ([]string, error) {
	owners := reg.Owners
	if len(owners) == 0 {
		if !registries.ValidOwner(reg.Username) {
			return nil, ErrBadOwner
		}
		owners = []string{reg.Username}
	}
	seen := map[string]bool{}
	for _, owner := range owners {
		if !registries.ValidOwner(owner) {
			return nil, ErrBadOwner
		}
		if err := c.forgejoOwner(ctx, reg, owner, seen); err != nil {
			return nil, err
		}
	}
	return sortedKeys(seen), nil
}

func (c *Client) forgejoOwner(ctx context.Context, reg Registry, owner string, seen map[string]bool) error {
	listed := 0
	for page := 1; page <= maxPages; page++ {
		u := base(reg)
		u.Path = "/api/v1/packages/" + owner
		u.RawQuery = url.Values{"type": {"container"}, "limit": {strconv.Itoa(forgejoPageSize)}, "page": {strconv.Itoa(page)}}.Encode()
		resp, err := c.forgejo(ctx, reg, http.MethodGet, u)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			err := statusError(resp)
			drain(resp)
			return fmt.Errorf("packages of %s: %w", owner, err)
		}
		var pkgs []forgejoPackage
		err = readJSON(resp, maxJSONBody, &pkgs)
		total, totalErr := strconv.Atoi(resp.Header.Get("X-Total-Count"))
		drain(resp)
		if err != nil {
			return err
		}
		for _, p := range pkgs {
			name := strings.ToLower(owner) + "/" + p.Name
			if p.Type == "container" && ValidRepository(name) {
				seen[name] = true
			}
		}
		listed += len(pkgs)
		if len(pkgs) == 0 || (totalErr == nil && listed >= total) || (totalErr != nil && len(pkgs) < forgejoPageSize) {
			return nil
		}
	}
	return nil
}

// Tags lists repo's tags, sorted. Entries that aren't tags (Forgejo lists platform
// manifests as sha256:… versions) are left out.
func (c *Client) Tags(ctx context.Context, reg Registry, repo string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.tags(ctx, reg, repo)
}

func (c *Client) tags(ctx context.Context, reg Registry, repo string) ([]string, error) {
	repo = repoName(reg, repo)
	root := base(reg)
	u := v2(reg, repo+"/tags/list")
	u.RawQuery = url.Values{"n": {strconv.Itoa(catalogPageSize)}}.Encode()
	seen := map[string]bool{}
	for page := 0; page < maxPages && u != nil; page++ {
		resp, err := c.do(ctx, reg, http.MethodGet, u, jsonAccept, pullScope(repo))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			err := statusError(resp)
			drain(resp)
			return nil, err
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = readJSON(resp, maxJSONBody, &body)
		next := nextLink(resp, u, root)
		drain(resp)
		if err != nil {
			return nil, err
		}
		for _, t := range body.Tags {
			if ValidTag(t) {
				seen[t] = true
			}
		}
		u = next
	}
	return sortedKeys(seen), nil
}

func pullScope(repo string) string   { return "repository:" + repo + ":pull" }
func deleteScope(repo string) string { return "repository:" + repo + ":delete" }

// descriptor points to a blob or manifest.
type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *platformDoc      `json:"platform"`
	Annotations map[string]string `json:"annotations"`
}

type platformDoc struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
}

// manifestDoc is an image manifest or an index (manifest list).
type manifestDoc struct {
	MediaType string       `json:"mediaType"`
	Config    *descriptor  `json:"config"`
	Layers    []descriptor `json:"layers"`
	Manifests []descriptor `json:"manifests"`
}

func (m manifestDoc) size() int64 {
	var n int64
	if m.Config != nil {
		n = m.Config.Size
	}
	for _, l := range m.Layers {
		n += l.Size
	}
	return n
}

// Manifest describes repo:tag: its digest and media type, and either its size and
// creation time (an image) or its platforms (an index).
func (c *Client) Manifest(ctx context.Context, reg Registry, repo, tag string) (Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	repo = repoName(reg, repo)
	doc, m, err := c.manifest(ctx, reg, repo, tag)
	if err != nil {
		return Manifest{}, err
	}
	switch m.MediaType {
	case MediaOCIIndex, MediaDockerList:
		m.Platforms = []Platform{}
		for _, d := range doc.Manifests {
			if len(m.Platforms) == maxPlatforms {
				break
			}
			if d.Annotations["vnd.docker.reference.type"] == "attestation-manifest" || !ValidDigest(d.Digest) ||
				(d.Platform != nil && d.Platform.OS == "unknown") {
				continue
			}
			child, _, err := c.manifest(ctx, reg, repo, d.Digest)
			if err != nil {
				return Manifest{}, fmt.Errorf("platform manifest: %w", err)
			}
			p := Platform{Digest: d.Digest, Size: child.size()}
			if d.Platform != nil {
				p.OS, p.Architecture, p.Variant = d.Platform.OS, d.Platform.Architecture, d.Platform.Variant
			}
			m.Platforms = append(m.Platforms, p)
		}
	default:
		m.Size = doc.size()
		if doc.Config != nil && ValidDigest(doc.Config.Digest) {
			// The creation time is a nicety: a config that can't be read leaves it out.
			m.Created, _ = c.created(ctx, reg, repo, doc.Config.Digest)
		}
	}
	return m, nil
}

// manifest fetches and parses repo's manifest ref (a tag or digest).
func (c *Client) manifest(ctx context.Context, reg Registry, repo, ref string) (manifestDoc, Manifest, error) {
	resp, err := c.do(ctx, reg, http.MethodGet, v2(reg, repo+"/manifests/"+ref), manifestAccept, pullScope(repo))
	if err != nil {
		return manifestDoc{}, Manifest{}, err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return manifestDoc{}, Manifest{}, statusError(resp)
	}
	body, err := readAll(resp, maxManifestBody)
	if err != nil {
		return manifestDoc{}, Manifest{}, err
	}
	var doc manifestDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return manifestDoc{}, Manifest{}, fmt.Errorf("%w: invalid manifest", ErrUnreachable)
	}
	m := Manifest{Digest: resp.Header.Get("Docker-Content-Digest"), MediaType: doc.MediaType}
	if !ValidDigest(m.Digest) {
		sum := sha256.Sum256(body)
		m.Digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	if m.MediaType == "" {
		m.MediaType, _, _ = mime.ParseMediaType(resp.Header.Get("Content-Type"))
	}
	switch m.MediaType {
	case MediaOCIIndex, MediaDockerList, MediaOCIManifest, MediaDockerV2:
	default:
		return manifestDoc{}, Manifest{}, fmt.Errorf("%w: unsupported manifest type", ErrUnreachable)
	}
	return doc, m, nil
}

// created reads the creation time from an image config blob.
func (c *Client) created(ctx context.Context, reg Registry, repo, dgst string) (time.Time, error) {
	resp, err := c.do(ctx, reg, http.MethodGet, v2(reg, repo+"/blobs/"+dgst), "*/*", pullScope(repo))
	if err != nil {
		return time.Time{}, err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, statusError(resp)
	}
	var cfg struct {
		Created time.Time `json:"created"`
	}
	if err := readJSON(resp, maxManifestBody, &cfg); err != nil {
		return time.Time{}, err
	}
	return cfg.Created, nil
}

// Digest returns the digest repo:tag points to now.
func (c *Client) Digest(ctx context.Context, reg Registry, repo, tag string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.digest(ctx, reg, repoName(reg, repo), tag)
}

func (c *Client) digest(ctx context.Context, reg Registry, repo, tag string) (string, error) {
	resp, err := c.do(ctx, reg, http.MethodHead, v2(reg, repo+"/manifests/"+tag), manifestAccept, pullScope(repo))
	if err != nil {
		return "", err
	}
	drain(resp)
	if resp.StatusCode != http.StatusOK {
		return "", statusError(resp)
	}
	if d := resp.Header.Get("Docker-Content-Digest"); ValidDigest(d) {
		return d, nil
	}
	// Without the header, the digest is the hash of the manifest as served.
	_, m, err := c.manifest(ctx, reg, repo, tag)
	return m.Digest, err
}

// SharedTags returns repo's tags other than tag that point to dgst, and whether every
// tag was checked (at most maxSharedLookups are; tags that fail to resolve are skipped).
func (c *Client) SharedTags(ctx context.Context, reg Registry, repo, tag, dgst string) ([]string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	repo = repoName(reg, repo)
	tags, err := c.tags(ctx, reg, repo)
	if err != nil {
		return nil, false, err
	}
	tags = slices.DeleteFunc(tags, func(t string) bool { return t == tag })
	complete := len(tags) <= maxSharedLookups
	tags = tags[:min(len(tags), maxSharedLookups)]
	var (
		mu     sync.Mutex
		shared = []string{}
		wg     sync.WaitGroup
		next   = make(chan string)
	)
	for range sharedLookupWorkers {
		wg.Go(func() {
			for t := range next {
				d, err := c.digest(ctx, reg, repo, t)
				mu.Lock()
				if err != nil {
					complete = false
				} else if d == dgst {
					shared = append(shared, t)
				}
				mu.Unlock()
			}
		})
	}
	for _, t := range tags {
		next <- t
	}
	close(next)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	slices.Sort(shared)
	return shared, complete, nil
}

// Delete removes repo:tag if it still points to dgst. On a generic registry that
// deletes the manifest, and with it every tag on dgst; on Forgejo only the tag's
// package version.
func (c *Client) Delete(ctx context.Context, reg Registry, repo, tag, dgst string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	repo = repoName(reg, repo)
	current, err := c.digest(ctx, reg, repo, tag)
	if err != nil {
		return err
	}
	if current != dgst {
		return ErrDigestMismatch
	}
	if reg.Kind == registries.Forgejo {
		return c.forgejoDelete(ctx, reg, repo, tag)
	}
	resp, err := c.do(ctx, reg, http.MethodDelete, v2(reg, repo+"/manifests/"+dgst), jsonAccept, deleteScope(repo))
	if err != nil {
		return err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusMethodNotAllowed:
		return fmt.Errorf("%w: delete answered 405", ErrDeleteUnsupported)
	}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusNotFound &&
		slices.Contains(errorCodes(resp), "UNSUPPORTED") {
		return fmt.Errorf("%w: delete unsupported", ErrDeleteUnsupported)
	}
	return statusError(resp)
}

// forgejoDelete deletes the package version of repo (owner/name) named tag.
func (c *Client) forgejoDelete(ctx context.Context, reg Registry, repo, tag string) error {
	owner, name, ok := splitOwner(repo)
	if !ok {
		return ErrNotFound
	}
	u := base(reg)
	u.Path = "/api/v1/packages/" + owner + "/container/" + name + "/" + tag
	// A nested image name keeps its slashes escaped: it is one path segment.
	u.RawPath = "/api/v1/packages/" + owner + "/container/" + url.PathEscape(name) + "/" + tag
	resp, err := c.forgejo(ctx, reg, http.MethodDelete, u)
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return statusError(resp)
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
