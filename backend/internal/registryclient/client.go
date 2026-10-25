// Package registryclient browses container registries over HTTP: the Docker Registry
// v2 API (catalog, tags, manifests, deletes) with its token and Basic auth, and for
// Forgejo the packages API, which lists a user's images and deletes one tag.
//
// Safety rules: HTTPS unless the registry is marked plain HTTP; credentials go to a
// token realm only over HTTPS (or to the registry's own host when it is plain HTTP);
// redirects never carry credentials to another host; every call has a timeout, bodies
// are capped and paging is bounded. Errors wrap the sentinels below and never contain
// response bodies or credentials.
package registryclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
)

var (
	// ErrNotFound means the registry has no such repository, tag or owner.
	ErrNotFound = errors.New("repository or tag not found")
	// ErrUnauthorized means the registry (or its token server) refused the credentials.
	ErrUnauthorized = errors.New("registry refused the credentials")
	// ErrUnreachable means the registry couldn't be reached or answered unexpectedly.
	ErrUnreachable = errors.New("registry unreachable")
	// ErrNoCatalog means the registry doesn't list its repositories (Docker Hub, GHCR).
	ErrNoCatalog = errors.New("registry doesn't list repositories; open one by name")
	// ErrDeleteUnsupported means the registry refuses manifest deletes.
	ErrDeleteUnsupported = errors.New("registry doesn't allow deletes")
	// ErrDigestMismatch means the tag no longer points to the digest the delete named.
	ErrDigestMismatch = errors.New("the tag points to another digest now")
	// ErrInsecureRealm means the registry asked for credentials at an address nudge
	// won't send them to.
	ErrInsecureRealm = errors.New("registry's token server isn't HTTPS")
	// ErrBadOwner means a Forgejo registry has no owners and its username isn't one.
	ErrBadOwner = errors.New("registry's username isn't a Forgejo user; set its owners")
)

// Media types of the manifests nudge reads.
const (
	MediaOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaDockerV2       = "application/vnd.docker.distribution.manifest.v2+json"
	manifestAccept      = MediaOCIIndex + ", " + MediaOCIManifest + ", " + MediaDockerList + ", " + MediaDockerV2
	jsonAccept          = "application/json"
	dockerHubAPIHost    = "registry-1.docker.io"
	defaultTimeout      = 30 * time.Second
	maxRedirects        = 5
	maxJSONBody         = 4 << 20
	maxManifestBody     = 4 << 20
	maxErrorBody        = 64 << 10
	maxPages            = 20
	catalogPageSize     = 1000
	forgejoPageSize     = 50
	maxPlatforms        = 32
	maxSharedLookups    = 100
	sharedLookupWorkers = 4
)

// Registry is a stored registry with its credentials, as the client needs it.
type Registry struct {
	Address   string
	Kind      registries.Kind
	Owners    []string
	PlainHTTP bool
	Username  string
	Password  string
}

// String names the registry without its credentials, should it end up in a log.
func (r Registry) String() string { return r.Address }

// Manifest describes a tag.
type Manifest struct {
	Digest    string
	MediaType string
	// Size is the config plus the layers; 0 for an index (see Platforms).
	Size int64
	// Created is the image's creation time from its config, zero when unknown or for an
	// index.
	Created time.Time
	// Platforms are an index's images, without attestations.
	Platforms []Platform
}

// Platform is one image of an index.
type Platform struct {
	OS           string
	Architecture string
	Variant      string
	Digest       string
	Size         int64
}

// Options are the Client's dependencies.
type Options struct {
	// Transport makes the requests; nil means a clone of http.DefaultTransport.
	Transport http.RoundTripper
	// Now is the clock for token expiry (time.Now by default).
	Now func() time.Time
	// Timeout bounds each operation (30 s by default).
	Timeout time.Duration
}

// Client talks to registries. It caches auth tokens; it is safe for concurrent use.
type Client struct {
	http    *http.Client
	now     func() time.Time
	timeout time.Duration

	mu     sync.Mutex
	tokens map[tokenKey]cachedToken
}

// New returns a Client.
func New(opts Options) *Client {
	transport := opts.Transport
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	c := &Client{now: opts.Now, timeout: opts.Timeout, tokens: map[tokenKey]cachedToken{}}
	if c.now == nil {
		c.now = time.Now
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	c.http = &http.Client{Transport: transport, CheckRedirect: checkRedirect}
	return c
}

// checkRedirect follows a few redirects, never from HTTPS to plain HTTP or to another
// plain HTTP host, and drops the credentials when the host changes (blob downloads
// redirect to storage elsewhere).
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("too many redirects")
	}
	first := via[0]
	switch {
	case req.URL.Scheme == "https":
	case req.URL.Scheme == "http" && first.URL.Scheme == "http" && strings.EqualFold(req.URL.Host, first.URL.Host):
	default:
		return errors.New("redirect to an insecure address refused")
	}
	if !strings.EqualFold(req.URL.Host, first.URL.Host) {
		req.Header.Del("Authorization")
	}
	return nil
}

// base is the registry's API root.
func base(reg Registry) *url.URL {
	u := &url.URL{Scheme: "https", Host: reg.Address, Path: "/"}
	if reg.PlainHTTP {
		u.Scheme = "http"
	}
	if reg.Address == registries.DockerHub {
		u.Host = dockerHubAPIHost
	}
	return u
}

// repoName is repo as the registry names it: Docker Hub's official images are library/….
func repoName(reg Registry, repo string) string {
	if reg.Address == registries.DockerHub && !strings.Contains(repo, "/") {
		return "library/" + repo
	}
	return repo
}

// v2 is the URL of the registry's /v2/<path>; path parts are validated names.
func v2(reg Registry, path string) *url.URL {
	u := base(reg)
	u.Path = "/v2/" + path
	return u
}

// do sends a registry request, answering one 401 with its challenge (Bearer token or
// Basic auth) and retrying. scope names the cached token the request uses.
func (c *Client) do(ctx context.Context, reg Registry, method string, u *url.URL, accept, scope string) (*http.Response, error) {
	key := c.keyFor(reg, scope)
	header, _ := c.cached(key)
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %s %s: %w", ErrUnreachable, method, u.Path, err)
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("User-Agent", "nudge")
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %s %s: %w", ErrUnreachable, method, u.Path, err)
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp, nil
		}
		ch, ok := parseChallenges(resp.Header)
		drain(resp)
		if !ok {
			return nil, fmt.Errorf("%w: %s %s answered 401 without a challenge", ErrUnauthorized, method, u.Path)
		}
		if header, err = c.authorize(ctx, reg, base(reg), ch, key); err != nil {
			return nil, err
		}
	}
}

// forgejo sends a request to Forgejo's API with Basic auth (the stored password is a
// Forgejo token).
func (c *Client) forgejo(ctx context.Context, reg Registry, method string, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s %s: %w", ErrUnreachable, method, u.Path, err)
	}
	req.Header.Set("Accept", jsonAccept)
	req.Header.Set("User-Agent", "nudge")
	req.SetBasicAuth(reg.Username, reg.Password)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s %s: %w", ErrUnreachable, method, u.Path, err)
	}
	return resp, nil
}

// statusError is the error for resp's unexpected status.
func statusError(resp *http.Response) error {
	var sentinel error
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		sentinel = ErrUnauthorized
	case http.StatusNotFound:
		sentinel = ErrNotFound
	default:
		sentinel = ErrUnreachable
	}
	return fmt.Errorf("%w: %s %s answered %d", sentinel, resp.Request.Method, resp.Request.URL.Path, resp.StatusCode)
}

// errorCodes returns the codes of a registry error body ({"errors":[{"code":…}]}).
func errorCodes(resp *http.Response) []string {
	body, err := readAll(resp, maxErrorBody)
	if err != nil {
		return nil
	}
	var e struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil {
		return nil
	}
	codes := make([]string, len(e.Errors))
	for i, x := range e.Errors {
		codes[i] = x.Code
	}
	return codes
}

// readAll reads resp's body, failing if it is larger than limit.
func readAll(resp *http.Response, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading the response: %w", ErrUnreachable, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: response larger than %d bytes", ErrUnreachable, limit)
	}
	return b, nil
}

// readJSON decodes resp's body (at most limit bytes) into v.
func readJSON(resp *http.Response, limit int64, v any) error {
	b, err := readAll(resp, limit)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w: %s sent invalid JSON", ErrUnreachable, resp.Request.URL.Path)
	}
	return nil
}

// drain reads a little of resp's body, so the connection can be reused, and closes it.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
	_ = resp.Body.Close()
}

func basicAuth(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

// nextLink returns the rel="next" URL of resp's Link header, resolved against cur, or
// nil if there is none or it leaves the registry.
func nextLink(resp *http.Response, cur, root *url.URL) *url.URL {
	for _, h := range resp.Header.Values("Link") {
		for part := range strings.SplitSeq(h, ",") {
			target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
			if !ok || !strings.Contains(strings.ReplaceAll(params, " ", ""), `rel="next"`) {
				continue
			}
			target = strings.TrimSpace(target)
			if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
				continue
			}
			u, err := url.Parse(target[1 : len(target)-1])
			if err != nil {
				return nil
			}
			next := cur.ResolveReference(u)
			if next.Scheme != root.Scheme || !strings.EqualFold(next.Host, root.Host) {
				return nil
			}
			return next
		}
	}
	return nil
}
