package registryclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
)

const (
	testUser     = "bot"
	testPassword = "s3cret-password"
	// bodyMarker is in every error body the fake sends; errors must never contain it.
	bodyMarker = "upstream-detail-marker"
)

// fakeRegistry is a Docker Registry v2 server with token or Basic auth, and optionally
// Forgejo's packages API.
type fakeRegistry struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// auth is "bearer", "basic" or "" (none).
	auth string
	// realm overrides the token realm (default <server>/token).
	realm string
	// tokenTTL is the expires_in of issued tokens (0 leaves it out).
	tokenTTL   int
	tokenCalls int
	tokens     map[string]bool
	// catalog is "ok", "missing" (404) or "token-refused" (401 to any token).
	catalog  string
	pageSize int
	// endlessCatalog answers every catalog page with a next link.
	endlessCatalog bool
	catalogCalls   int
	deletes        bool
	// tags[repo][tag] = digest; manifests and blobs by digest.
	tags      map[string]map[string]string
	manifests map[string]fakeManifest
	blobs     map[string][]byte
	// blobRedirect sends blob downloads to this base URL.
	blobRedirect string
	// noDigestHeader leaves Docker-Content-Digest out.
	noDigestHeader bool
	// forgejo serves /api/v1/packages.
	forgejo bool
	// packagePage is the packages API's page size cap (Forgejo's MAX_RESPONSE_ITEMS).
	packagePage int
	requests    []string
}

type fakeManifest struct {
	mediaType string
	body      []byte
}

func newFakeRegistry(t *testing.T, auth string, tls bool) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{
		t: t, auth: auth, catalog: "ok", pageSize: 100, deletes: true, packagePage: 50,
		tokens: map[string]bool{}, tags: map[string]map[string]string{},
		manifests: map[string]fakeManifest{}, blobs: map[string][]byte{},
	}
	f.srv = httptest.NewUnstartedServer(f)
	// Clients the tests cut off (timeouts, refused redirects) would log handshake errors.
	f.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	if tls {
		f.srv.StartTLS()
	} else {
		f.srv.Start()
	}
	t.Cleanup(f.srv.Close)
	return f
}

// registry is the client's view of the fake.
func (f *fakeRegistry) registry() Registry {
	u, _ := url.Parse(f.srv.URL)
	return Registry{Address: u.Host, Kind: registries.Generic, Username: testUser, Password: testPassword,
		PlainHTTP: u.Scheme == "http"}
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// addImage stores a single-platform image with layers of the given sizes and tags it.
func (f *fakeRegistry) addImage(repo, tag, created string, layers ...int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := mustJSON(f.t, map[string]any{"created": created, "architecture": "amd64", "os": "linux"})
	cfgDigest := digestOf(cfg)
	f.blobs[cfgDigest] = cfg
	ls := []map[string]any{}
	for i, size := range layers {
		ls = append(ls, map[string]any{"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip", "size": size,
			"digest": digestOf([]byte(repo + tag + strconv.Itoa(i)))})
	}
	body := mustJSON(f.t, map[string]any{"schemaVersion": 2, "mediaType": MediaDockerV2,
		"config": map[string]any{"mediaType": "application/vnd.docker.container.image.v1+json", "size": len(cfg), "digest": cfgDigest},
		"layers": ls})
	d := digestOf(body)
	f.manifests[d] = fakeManifest{mediaType: MediaDockerV2, body: body}
	f.tagLocked(repo, tag, d)
	return d
}

// addIndex stores an OCI index of the given child digests (platform "os/arch[/variant]")
// plus a buildx attestation manifest, and tags it.
func (f *fakeRegistry) addIndex(repo, tag string, children map[string]string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ms := []map[string]any{}
	for _, plat := range slices.Sorted(maps.Keys(children)) {
		parts := strings.Split(plat, "/")
		p := map[string]any{"os": parts[0], "architecture": parts[1]}
		if len(parts) > 2 {
			p["variant"] = parts[2]
		}
		d := children[plat]
		ms = append(ms, map[string]any{"mediaType": MediaOCIManifest, "digest": d, "size": len(f.manifests[d].body), "platform": p})
	}
	ms = append(ms, map[string]any{"mediaType": MediaOCIManifest, "digest": digestOf([]byte("attestation")), "size": 100,
		"platform":    map[string]any{"os": "unknown", "architecture": "unknown"},
		"annotations": map[string]any{"vnd.docker.reference.type": "attestation-manifest"}})
	body := mustJSON(f.t, map[string]any{"schemaVersion": 2, "mediaType": MediaOCIIndex, "manifests": ms})
	d := digestOf(body)
	f.manifests[d] = fakeManifest{mediaType: MediaOCIIndex, body: body}
	f.tagLocked(repo, tag, d)
	return d
}

func (f *fakeRegistry) tag(repo, tag, d string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tagLocked(repo, tag, d)
}

func (f *fakeRegistry) tagLocked(repo, tag, d string) {
	if f.tags[repo] == nil {
		f.tags[repo] = map[string]string{}
	}
	if tag != "" {
		f.tags[repo][tag] = d
	}
}

func (f *fakeRegistry) tagsOf(repo string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.tags[repo])
}

func registryError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"errors":[{"code":%q,"message":%q}]}`, code, bodyMarker)
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	switch {
	case r.URL.Path == "/token":
		f.serveToken(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/packages/") && f.forgejo:
		f.servePackages(w, r)
	case strings.HasPrefix(r.URL.Path, "/v2/"):
		if !f.authorized(r) {
			f.challenge(w)
			return
		}
		f.serveV2(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeRegistry) serveToken(w http.ResponseWriter, r *http.Request) {
	f.tokenCalls++
	if u, p, ok := r.BasicAuth(); !ok || u != testUser || p != testPassword {
		registryError(w, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	token := "tok-" + strconv.Itoa(f.tokenCalls)
	f.tokens[token] = true
	resp := map[string]any{"token": token}
	if f.tokenTTL > 0 {
		resp["expires_in"] = f.tokenTTL
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeRegistry) authorized(r *http.Request) bool {
	switch f.auth {
	case "bearer":
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		return ok && f.tokens[token]
	case "basic":
		u, p, ok := r.BasicAuth()
		return ok && u == testUser && p == testPassword
	}
	return true
}

func (f *fakeRegistry) challenge(w http.ResponseWriter) {
	switch f.auth {
	case "bearer":
		realm := f.realm
		if realm == "" {
			realm = f.srv.URL + "/token"
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="container_registry",scope="*"`, realm))
	case "basic":
		w.Header().Set("WWW-Authenticate", `Basic realm="Registry Realm"`)
	}
	registryError(w, http.StatusUnauthorized, "UNAUTHORIZED")
}

func (f *fakeRegistry) serveV2(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	if path == "_catalog" {
		f.serveCatalog(w, r)
		return
	}
	if repo, ok := strings.CutSuffix(path, "/tags/list"); ok {
		tags, ok := f.tags[repo]
		if !ok {
			registryError(w, http.StatusNotFound, "NAME_UNKNOWN")
			return
		}
		list := []string{}
		for t := range tags {
			list = append(list, t)
		}
		if f.forgejo {
			// Forgejo's tag list shows platform manifests as versions too.
			for _, d := range tags {
				var idx manifestDoc
				if err := json.Unmarshal(f.manifests[d].body, &idx); err == nil {
					for _, m := range idx.Manifests {
						list = append(list, m.Digest)
					}
				}
			}
		}
		slices.Sort(list)
		_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": list})
		return
	}
	if i := strings.LastIndex(path, "/blobs/"); i > 0 {
		d := path[i+len("/blobs/"):]
		if f.blobRedirect != "" {
			http.Redirect(w, r, f.blobRedirect+"/blob/"+d, http.StatusTemporaryRedirect)
			return
		}
		b, ok := f.blobs[d]
		if !ok {
			registryError(w, http.StatusNotFound, "BLOB_UNKNOWN")
			return
		}
		_, _ = w.Write(b)
		return
	}
	i := strings.LastIndex(path, "/manifests/")
	if i <= 0 {
		http.NotFound(w, r)
		return
	}
	repo, ref := path[:i], path[i+len("/manifests/"):]
	d := ref
	if !strings.HasPrefix(ref, "sha256:") {
		d = f.tags[repo][ref]
	}
	m, ok := f.manifests[d]
	if !ok || f.tags[repo] == nil {
		registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN")
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if !f.deletes {
			registryError(w, http.StatusMethodNotAllowed, "UNSUPPORTED")
			return
		}
		for t, td := range f.tags[repo] {
			if td == d {
				delete(f.tags[repo], t)
			}
		}
		w.WriteHeader(http.StatusAccepted)
	case http.MethodGet, http.MethodHead:
		w.Header().Set("Content-Type", m.mediaType)
		if !f.noDigestHeader {
			w.Header().Set("Docker-Content-Digest", d)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(m.body)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(m.body)
		}
	}
}

func (f *fakeRegistry) serveCatalog(w http.ResponseWriter, r *http.Request) {
	f.catalogCalls++
	switch f.catalog {
	case "missing":
		registryError(w, http.StatusNotFound, "NOT_FOUND")
		return
	case "token-refused":
		f.challenge(w)
		return
	}
	if f.endlessCatalog {
		w.Header().Set("Link", `</v2/_catalog?last=x&n=1>; rel="next"`)
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []string{"r" + strconv.Itoa(f.catalogCalls)}})
		return
	}
	names := []string{}
	for repo := range f.tags {
		names = append(names, repo)
	}
	slices.Sort(names)
	last := r.URL.Query().Get("last")
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	n = min(n, f.pageSize)
	start, _ := slices.BinarySearch(names, last)
	if last != "" && start < len(names) && names[start] == last {
		start++
	}
	page := names[start:min(start+n, len(names))]
	if start+n < len(names) {
		w.Header().Set("Link", fmt.Sprintf(`</v2/_catalog?last=%s&n=%d>; rel="next"`, url.QueryEscape(page[len(page)-1]), n))
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"repositories": page})
}

// forgejoUser mirrors the user object of Forgejo's API (structs.User), as the packages
// API embeds it for owner and creator.
func forgejoUser(login string) map[string]any {
	return map[string]any{
		"id": 1, "login": login, "login_name": "", "source_id": 0, "full_name": "", "email": login + "@noreply.example.com",
		"avatar_url": "https://git.example.com/avatars/1", "html_url": "https://git.example.com/" + login, "language": "",
		"is_admin": false, "last_login": "0001-01-01T00:00:00Z", "created": "2026-01-01T00:00:00Z", "restricted": false,
		"active": false, "prohibit_login": false, "location": "", "pronouns": "", "website": "", "description": "",
		"visibility": "public", "followers_count": 0, "following_count": 0, "starred_repos_count": 0, "username": login,
	}
}

// servePackages is Forgejo's packages API: GET /api/v1/packages/{owner}?type=container
// lists one entry per package version (tags and, for multi-arch images, the platform
// manifests as sha256:… versions); DELETE /api/v1/packages/{owner}/container/{name}/{version}
// deletes one version.
func (f *fakeRegistry) servePackages(w http.ResponseWriter, r *http.Request) {
	// Forgejo takes any username with a valid token of that user; the fake checks the token.
	if _, p, ok := r.BasicAuth(); !ok || p != testPassword {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"message":%q}`, bodyMarker)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/packages/"), "/")
	owner := parts[0]
	if r.Method == http.MethodDelete {
		if len(parts) != 4 || parts[1] != "container" {
			http.NotFound(w, r)
			return
		}
		repo := owner + "/" + parts[2]
		if _, ok := f.tags[repo][parts[3]]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.tags[repo], parts[3])
		w.WriteHeader(http.StatusNoContent)
		return
	}
	type entry struct{ name, version string }
	var entries []entry
	for _, repo := range slices.Sorted(maps.Keys(f.tags)) {
		name, ok := strings.CutPrefix(repo, owner+"/")
		if !ok {
			continue
		}
		for _, t := range slices.Sorted(maps.Keys(f.tags[repo])) {
			entries = append(entries, entry{name, t})
			var idx manifestDoc
			if err := json.Unmarshal(f.manifests[f.tags[repo][t]].body, &idx); err == nil {
				for _, m := range idx.Manifests {
					entries = append(entries, entry{name, m.Digest})
				}
			}
		}
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	limit = min(limit, f.packagePage)
	start := min((page-1)*limit, len(entries))
	list := []map[string]any{}
	for i, e := range entries[start:min(start+limit, len(entries))] {
		list = append(list, map[string]any{
			"id": start + i + 1, "owner": forgejoUser(owner), "repository": nil, "creator": forgejoUser(owner),
			"type": "container", "name": e.name, "version": e.version,
			"html_url":   "https://git.example.com/" + owner + "/-/packages/container/" + e.name + "/" + url.PathEscape(e.version),
			"created_at": time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC),
		})
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(entries)))
	_ = json.NewEncoder(w).Encode(list)
}
