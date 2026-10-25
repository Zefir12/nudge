package registryclient

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClient(t *testing.T, f *fakeRegistry) (*Client, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	return New(Options{Transport: f.srv.Client().Transport, Now: clock.Now}), clock
}

// checkSafe fails if err leaks the password or an upstream body.
func checkSafe(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if msg := err.Error(); strings.Contains(msg, testPassword) || strings.Contains(msg, bodyMarker) {
		t.Errorf("error leaks credentials or upstream detail: %s", msg)
	}
}

func TestNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		repo bool
		tag  bool
	}{
		{name: "zefir/nudge", repo: true},
		{name: "a/b/c", repo: true},
		{name: "my-app_1.x", repo: true, tag: true},
		{name: "latest", repo: true, tag: true},
		{name: "v1.2.3", repo: true, tag: true},
		{name: "V1", tag: true},
		{name: "a__b", repo: true, tag: true},
		{name: "../etc"},
		{name: "a/../b"},
		{name: "a//b"},
		{name: "a%2Fb"},
		{name: "a/b/"},
		{name: "/a"},
		{name: ""},
		{name: "a b"},
		{name: "a?b"},
		{name: "sha256:" + strings.Repeat("a", 64)},
		{name: ".hidden"},
		{name: "-x", repo: false},
		{name: strings.Repeat("a", 129), repo: true},
		{name: strings.Repeat("a", 256)},
	}
	for _, tt := range tests {
		if got := ValidRepository(tt.name); got != tt.repo {
			t.Errorf("ValidRepository(%q) = %v", tt.name, got)
		}
		if got := ValidTag(tt.name); got != tt.tag {
			t.Errorf("ValidTag(%q) = %v", tt.name, got)
		}
	}
	for d, want := range map[string]bool{
		"sha256:" + strings.Repeat("a", 64):  true,
		"sha512:" + strings.Repeat("0", 128): true,
		"sha256:" + strings.Repeat("A", 64):  false,
		"sha256:abc":                         false,
		"md5:" + strings.Repeat("a", 32):     false,
	} {
		if got := ValidDigest(d); got != want {
			t.Errorf("ValidDigest(%q) = %v", d, got)
		}
	}
}

func TestParseChallenge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in     string
		scheme string
		params map[string]string
	}{
		{in: `Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull"`,
			scheme: "bearer", params: map[string]string{"realm": "https://auth.docker.io/token", "service": "registry.docker.io", "scope": "repository:library/nginx:pull"}},
		{in: `Bearer realm="https://git.example.com/v2/token",service="container_registry",scope="*"`,
			scheme: "bearer", params: map[string]string{"realm": "https://git.example.com/v2/token", "service": "container_registry", "scope": "*"}},
		{in: `Basic realm="Registry Realm"`, scheme: "basic", params: map[string]string{"realm": "Registry Realm"}},
		{in: `Bearer realm=https://x/token, error="insufficient_scope"`, scheme: "bearer",
			params: map[string]string{"realm": "https://x/token", "error": "insufficient_scope"}},
		{in: `Bearer realm="a\"b"`, scheme: "bearer", params: map[string]string{"realm": `a"b`}},
		{in: `Bearer realm="unterminated`, scheme: "bearer", params: map[string]string{"realm": "unterminated"}},
		{in: `Negotiate`, scheme: "negotiate", params: map[string]string{}},
	}
	for _, tt := range tests {
		c := parseChallenge(tt.in)
		if c.scheme != tt.scheme || len(c.params) != len(tt.params) {
			t.Errorf("parseChallenge(%q) = %+v", tt.in, c)
			continue
		}
		for k, v := range tt.params {
			if c.params[k] != v {
				t.Errorf("parseChallenge(%q)[%s] = %q, want %q", tt.in, k, c.params[k], v)
			}
		}
	}
}

func TestAuth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		auth     string
		password string
		wantErr  error
	}{
		{name: "bearer", auth: "bearer", password: testPassword},
		{name: "bearer wrong password", auth: "bearer", password: "wrong", wantErr: ErrUnauthorized},
		{name: "basic", auth: "basic", password: testPassword},
		{name: "basic wrong password", auth: "basic", password: "wrong", wantErr: ErrUnauthorized},
		{name: "anonymous", auth: "", password: testPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeRegistry(t, tt.auth, true)
			f.addImage("zefir/nudge", "latest", "2026-10-01T00:00:00Z", 10)
			f.addImage("zefir/nudge", "v1", "2026-10-01T00:00:00Z", 20)
			c, _ := newClient(t, f)
			reg := f.registry()
			reg.Password = tt.password
			tags, err := c.Tags(t.Context(), reg, "zefir/nudge")
			checkSafe(t, err)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(tags, []string{"latest", "v1"}) {
				t.Fatalf("Tags = %v, %v", tags, err)
			}
		})
	}
}

func TestTokenCache(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, "bearer", true)
	f.tokenTTL = 300
	f.addImage("zefir/nudge", "latest", "2026-10-01T00:00:00Z", 10)
	c, clock := newClient(t, f)
	reg := f.registry()
	calls := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.tokenCalls
	}
	for range 3 {
		if _, err := c.Tags(t.Context(), reg, "zefir/nudge"); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls(); n != 1 {
		t.Fatalf("token requests = %d, want 1 (cached)", n)
	}
	// Another password is another cache entry.
	other := reg
	other.Password = "wrong"
	if _, err := c.Tags(t.Context(), other, "zefir/nudge"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong password: %v", err)
	}
	if n := calls(); n != 2 {
		t.Fatalf("token requests = %d, want 2", n)
	}
	clock.advance(296 * time.Second)
	if _, err := c.Tags(t.Context(), reg, "zefir/nudge"); err != nil {
		t.Fatal(err)
	}
	if n := calls(); n != 3 {
		t.Fatalf("token requests after expiry = %d, want 3", n)
	}
}

func TestTokenCacheBounded(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Unix(1_700_000_000, 0)}
	c := New(Options{Now: clock.Now})
	for i := range maxTokens + 50 {
		c.store(c.keyFor(Registry{Address: "r", Username: "u"}, string(rune('a'+i%26))+strings.Repeat("x", i)), "h", clock.Now().Add(time.Duration(i+1)*time.Second))
	}
	if n := len(c.tokens); n != maxTokens {
		t.Fatalf("cached tokens = %d, want %d", n, maxTokens)
	}
	clock.advance(time.Hour)
	c.store(c.keyFor(Registry{Address: "r"}, "new"), "h", clock.Now().Add(time.Minute))
	if n := len(c.tokens); n != 1 {
		t.Fatalf("cached tokens after expiry = %d, want 1", n)
	}
}

func TestRealmRules(t *testing.T) {
	t.Parallel()
	other := newFakeRegistry(t, "", false) // a plain HTTP host that would take the credentials
	otherTLS := newFakeRegistry(t, "", true)
	tests := []struct {
		name    string
		tls     bool
		realm   func(f *fakeRegistry) string
		wantErr error
		// leak is the fake that must not receive a token request.
		leak *fakeRegistry
	}{
		{name: "plain registry, realm on another plain host", realm: func(*fakeRegistry) string { return other.srv.URL + "/token" },
			wantErr: ErrInsecureRealm, leak: other},
		{name: "plain registry, realm on its own host", realm: func(f *fakeRegistry) string { return f.srv.URL + "/token" }},
		{name: "https registry, plain realm on its own host", tls: true,
			realm: func(f *fakeRegistry) string { return strings.Replace(f.srv.URL, "https://", "http://", 1) + "/token" }, wantErr: ErrInsecureRealm},
		{name: "https registry, https realm elsewhere", tls: true, realm: func(*fakeRegistry) string { return otherTLS.srv.URL + "/token" },
			// otherTLS rejects the credentials it doesn't know, but it is asked.
			wantErr: ErrUnauthorized},
		{name: "realm with userinfo", tls: true, realm: func(*fakeRegistry) string { return "https://u:p@example.com/token" }, wantErr: ErrInsecureRealm},
		{name: "realm not http", tls: true, realm: func(*fakeRegistry) string { return "file:///etc/passwd" }, wantErr: ErrUnreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeRegistry(t, "bearer", tt.tls)
			f.realm = tt.realm(f)
			f.addImage("zefir/nudge", "latest", "2026-10-01T00:00:00Z", 10)
			c, _ := newClient(t, f)
			_, err := c.Tags(t.Context(), f.registry(), "zefir/nudge")
			checkSafe(t, err)
			if tt.wantErr == nil && err != nil || tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.leak != nil {
				tt.leak.mu.Lock()
				defer tt.leak.mu.Unlock()
				if len(tt.leak.requests) > 0 {
					t.Errorf("credentials sent to %v", tt.leak.requests)
				}
			}
		})
	}
}

func TestPlainHTTPNeedsFlag(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, "", false)
	f.addImage("zefir/nudge", "latest", "2026-10-01T00:00:00Z", 10)
	c, _ := newClient(t, f)
	reg := f.registry()
	reg.PlainHTTP = false
	if _, err := c.Tags(t.Context(), reg, "zefir/nudge"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("HTTPS to a plain HTTP registry: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) > 0 {
		t.Errorf("plain HTTP requests without the flag: %v", f.requests)
	}
}

func TestCatalog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		auth      string
		catalog   string
		endless   bool
		want      []string
		wantErr   error
		wantCalls int
	}{
		{name: "paged", auth: "bearer", catalog: "ok", want: []string{"a/one", "b/two", "c/three", "d/four", "e/five"}, wantCalls: 3},
		{name: "basic", auth: "basic", catalog: "ok", want: []string{"a/one", "b/two", "c/three", "d/four", "e/five"}, wantCalls: 3},
		{name: "missing", auth: "bearer", catalog: "missing", wantErr: ErrNoCatalog},
		{name: "refused to the token", auth: "bearer", catalog: "token-refused", wantErr: ErrNoCatalog},
		{name: "endless paging stops", auth: "", catalog: "ok", endless: true, wantCalls: maxPages},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeRegistry(t, tt.auth, true)
			f.catalog, f.pageSize, f.endlessCatalog = tt.catalog, 2, tt.endless
			for _, r := range []string{"c/three", "a/one", "e/five", "b/two", "d/four"} {
				f.addImage(r, "latest", "2026-10-01T00:00:00Z", 1)
			}
			c, _ := newClient(t, f)
			got, err := c.Repositories(t.Context(), f.registry())
			checkSafe(t, err)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.want != nil && !slices.Equal(got, tt.want) {
				t.Errorf("Repositories = %v, want %v", got, tt.want)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.catalogCalls != tt.wantCalls {
				t.Errorf("authorized catalog requests = %d, want %d", f.catalogCalls, tt.wantCalls)
			}
		})
	}
}

func TestNextLinkStaysOnRegistry(t *testing.T) {
	t.Parallel()
	root, _ := url.Parse("https://reg.example.com/")
	cur, _ := url.Parse("https://reg.example.com/v2/_catalog?n=2")
	tests := map[string]string{
		`</v2/_catalog?last=b&n=2>; rel="next"`:                     "https://reg.example.com/v2/_catalog?last=b&n=2",
		`<https://reg.example.com/v2/_catalog?last=b>; rel="next"`:  "https://reg.example.com/v2/_catalog?last=b",
		`<https://evil.example.com/v2/_catalog?last=b>; rel="next"`: "",
		`<http://reg.example.com/v2/_catalog?last=b>; rel="next"`:   "",
		`</v2/_catalog?last=b>; rel="prev"`:                         "",
		`</a>; rel="prev", </v2/_catalog?last=c>; rel="next"`:       "https://reg.example.com/v2/_catalog?last=c",
		`garbage`: "",
	}
	for h, want := range tests {
		resp := &http.Response{Header: http.Header{"Link": {h}}}
		got := nextLink(resp, cur, root)
		if (got == nil && want != "") || (got != nil && got.String() != want) {
			t.Errorf("nextLink(%s) = %v, want %q", h, got, want)
		}
	}
}

func TestManifest(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, "bearer", true)
	single := f.addImage("zefir/nudge", "latest", "2026-10-01T12:00:00Z", 100, 200)
	amd := f.addImage("zefir/nudge", "", "2026-10-01T12:00:00Z", 1000)
	arm := f.addImage("zefir/nudge", "", "2026-10-01T12:00:00Z", 2000, 3000)
	index := f.addIndex("zefir/nudge", "multi", map[string]string{"linux/amd64": amd, "linux/arm64/v8": arm})
	c, _ := newClient(t, f)
	reg := f.registry()

	m, err := c.Manifest(t.Context(), reg, "zefir/nudge", "latest")
	if err != nil {
		t.Fatal(err)
	}
	cfgSize := int64(len(f.blobs[func() string {
		for d := range f.blobs {
			return d
		}
		return ""
	}()]))
	if m.Digest != single || m.MediaType != MediaDockerV2 || m.Size != 300+cfgSize || len(m.Platforms) != 0 ||
		!m.Created.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("single manifest = %+v (config %d bytes)", m, cfgSize)
	}

	m, err = c.Manifest(t.Context(), reg, "zefir/nudge", "multi")
	if err != nil {
		t.Fatal(err)
	}
	if m.Digest != index || m.MediaType != MediaOCIIndex || m.Size != 0 || !m.Created.IsZero() || len(m.Platforms) != 2 {
		t.Fatalf("index = %+v", m)
	}
	if p := m.Platforms[0]; p.OS != "linux" || p.Architecture != "amd64" || p.Digest != amd || p.Size != 1000+cfgSize {
		t.Errorf("amd64 = %+v", p)
	}
	if p := m.Platforms[1]; p.Architecture != "arm64" || p.Variant != "v8" || p.Size != 5000+cfgSize {
		t.Errorf("arm64 = %+v", p)
	}

	if _, err := c.Manifest(t.Context(), reg, "zefir/nudge", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown tag: %v", err)
	}
	if _, err := c.Manifest(t.Context(), reg, "zefir/other", "latest"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown repository: %v", err)
	}
}

func TestOversizedBody(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, "", true)
	f.addImage("zefir/nudge", "latest", "2026-10-01T00:00:00Z", 1)
	big := []byte(`{"mediaType":"` + MediaDockerV2 + `","pad":"` + strings.Repeat("x", maxManifestBody) + `"}`)
	f.mu.Lock()
	f.manifests[digestOf(big)] = fakeManifest{mediaType: MediaDockerV2, body: big}
	f.tags["zefir/nudge"]["big"] = digestOf(big)
	f.mu.Unlock()
	c, _ := newClient(t, f)
	_, err := c.Manifest(t.Context(), f.registry(), "zefir/nudge", "big")
	checkSafe(t, err)
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized manifest: %v", err)
	}
}

func TestBlobRedirectDropsCredentials(t *testing.T) {
	t.Parallel()
	var gotAuth []string
	var mu sync.Mutex
	f := newFakeRegistry(t, "basic", true)
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		d := strings.TrimPrefix(r.URL.Path, "/blob/")
		f.mu.Lock()
		b := f.blobs[d]
		f.mu.Unlock()
		_, _ = w.Write(b)
	}))
	t.Cleanup(storage.Close)
	f.addImage("zefir/nudge", "latest", "2026-10-01T12:00:00Z", 1)
	f.blobRedirect = storage.URL
	c, _ := newClient(t, f)
	m, err := c.Manifest(t.Context(), f.registry(), "zefir/nudge", "latest")
	if err != nil || m.Created.IsZero() {
		t.Fatalf("Manifest = %+v, %v", m, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gotAuth) != 1 || gotAuth[0] != "" {
		t.Errorf("storage got Authorization %q", gotAuth)
	}
}

func TestCheckRedirect(t *testing.T) {
	t.Parallel()
	req := func(s string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, s, nil)
		r.Header.Set("Authorization", "Basic x")
		return r
	}
	tests := []struct {
		from, to string
		wantErr  bool
		keepAuth bool
	}{
		{from: "https://reg.example.com/v2/x", to: "https://reg.example.com/v2/y", keepAuth: true},
		{from: "https://reg.example.com/v2/x", to: "https://s3.example.com/blob"},
		{from: "https://reg.example.com/v2/x", to: "http://reg.example.com/v2/y", wantErr: true},
		{from: "http://reg.lan:5000/v2/x", to: "http://reg.lan:5000/v2/y", keepAuth: true},
		{from: "http://reg.lan:5000/v2/x", to: "http://other.lan/blob", wantErr: true},
		{from: "http://reg.lan:5000/v2/x", to: "https://s3.example.com/blob"},
	}
	for _, tt := range tests {
		next := req(tt.to)
		err := checkRedirect(next, []*http.Request{req(tt.from)})
		if (err != nil) != tt.wantErr {
			t.Errorf("%s → %s: err = %v", tt.from, tt.to, err)
			continue
		}
		if err == nil && (next.Header.Get("Authorization") != "") != tt.keepAuth {
			t.Errorf("%s → %s: Authorization kept = %v", tt.from, tt.to, !tt.keepAuth)
		}
	}
	via := []*http.Request{req("https://a/"), req("https://a/"), req("https://a/"), req("https://a/"), req("https://a/")}
	if checkRedirect(req("https://a/"), via) == nil {
		t.Error("followed too many redirects")
	}
}

func TestDigestWithoutHeader(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, "", true)
	want := f.addImage("zefir/nudge", "latest", "2026-10-01T00:00:00Z", 1)
	f.noDigestHeader = true
	c, _ := newClient(t, f)
	got, err := c.Digest(t.Context(), f.registry(), "zefir/nudge", "latest")
	if err != nil || got != want {
		t.Fatalf("Digest = %q, %v; want %q", got, err, want)
	}
}

func TestSharedTags(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, "bearer", true)
	d := f.addImage("zefir/nudge", "latest", "2026-10-01T00:00:00Z", 1)
	f.tag("zefir/nudge", "v1.2", d)
	f.tag("zefir/nudge", "v1", d)
	f.addImage("zefir/nudge", "v0.9", "2026-09-01T00:00:00Z", 2)
	c, _ := newClient(t, f)
	shared, complete, err := c.SharedTags(t.Context(), f.registry(), "zefir/nudge", "latest", d)
	if err != nil || !complete || !slices.Equal(shared, []string{"v1", "v1.2"}) {
		t.Fatalf("SharedTags = %v, %v, %v", shared, complete, err)
	}
}

func TestDeleteGeneric(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		deletes  bool
		digest   func(d, other string) string
		wantErr  error
		wantTags []string
	}{
		{name: "deletes every tag on the digest", deletes: true, digest: func(d, _ string) string { return d }, wantTags: []string{"v0.9"}},
		{name: "digest moved", deletes: true, digest: func(_, other string) string { return other }, wantErr: ErrDigestMismatch,
			wantTags: []string{"latest", "v0.9", "v1"}},
		{name: "deletes disabled", deletes: false, digest: func(d, _ string) string { return d }, wantErr: ErrDeleteUnsupported,
			wantTags: []string{"latest", "v0.9", "v1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeRegistry(t, "bearer", true)
			f.deletes = tt.deletes
			d := f.addImage("zefir/nudge", "latest", "2026-10-01T00:00:00Z", 1)
			f.tag("zefir/nudge", "v1", d)
			other := f.addImage("zefir/nudge", "v0.9", "2026-09-01T00:00:00Z", 2)
			c, _ := newClient(t, f)
			err := c.Delete(t.Context(), f.registry(), "zefir/nudge", "latest", tt.digest(d, other))
			checkSafe(t, err)
			if tt.wantErr == nil && err != nil || tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			tags := f.tagsOf("zefir/nudge")
			got := make([]string, 0, len(tags))
			for tag := range tags {
				got = append(got, tag)
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.wantTags) {
				t.Errorf("tags left = %v, want %v", got, tt.wantTags)
			}
		})
	}
}

func newForgejo(t *testing.T) (*fakeRegistry, Registry) {
	t.Helper()
	f := newFakeRegistry(t, "bearer", true)
	f.forgejo = true
	reg := f.registry()
	reg.Kind = registries.Forgejo
	return f, reg
}

func TestForgejoRepositories(t *testing.T) {
	t.Parallel()
	f, reg := newForgejo(t)
	f.packagePage = 3 // smaller than the client's limit: paging must follow X-Total-Count
	amd := f.addImage("zefir/nudge", "", "2026-10-01T00:00:00Z", 1)
	arm := f.addImage("zefir/nudge", "", "2026-10-01T00:00:00Z", 2)
	f.addIndex("zefir/nudge", "latest", map[string]string{"linux/amd64": amd, "linux/arm64": arm})
	f.addImage("zefir/nudge", "v1", "2026-10-01T00:00:00Z", 1)
	f.addImage("zefir/tools", "latest", "2026-10-01T00:00:00Z", 1)
	f.addImage("team/app", "latest", "2026-10-01T00:00:00Z", 1)
	f.addImage("someone/else", "latest", "2026-10-01T00:00:00Z", 1)
	c, _ := newClient(t, f)

	tests := []struct {
		name     string
		username string
		owners   []string
		password string
		want     []string
		wantErr  error
	}{
		{name: "owners", owners: []string{"zefir", "team"}, password: testPassword, want: []string{"team/app", "zefir/nudge", "zefir/tools"}},
		{name: "default owner is the username", username: "zefir", password: testPassword, want: []string{"zefir/nudge", "zefir/tools"}},
		{name: "username isn't an owner", username: "a/../b", password: testPassword, wantErr: ErrBadOwner},
		{name: "wrong token", owners: []string{"zefir"}, password: "wrong", wantErr: ErrUnauthorized},
	}
	for _, tt := range tests {
		r := reg
		r.Owners, r.Password = tt.owners, tt.password
		if tt.username != "" {
			r.Username = tt.username
		}
		got, err := c.Repositories(t.Context(), r)
		checkSafe(t, err)
		if tt.wantErr != nil {
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("%s: err = %v, want %v", tt.name, err, tt.wantErr)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("%s: Repositories = %v, %v; want %v", tt.name, got, err, tt.want)
		}
	}
}

func TestForgejoTagsAndDelete(t *testing.T) {
	t.Parallel()
	f, reg := newForgejo(t)
	amd := f.addImage("zefir/nudge", "", "2026-10-01T00:00:00Z", 1)
	arm := f.addImage("zefir/nudge", "", "2026-10-01T00:00:00Z", 2)
	d := f.addIndex("zefir/nudge", "latest", map[string]string{"linux/amd64": amd, "linux/arm64": arm})
	f.tag("zefir/nudge", "v1", d)
	f.addImage("zefir/nudge", "v0.9", "2026-09-01T00:00:00Z", 1)
	c, _ := newClient(t, f)

	tags, err := c.Tags(t.Context(), reg, "zefir/nudge")
	if err != nil || !slices.Equal(tags, []string{"latest", "v0.9", "v1"}) {
		t.Fatalf("Tags = %v, %v (platform digests must be hidden)", tags, err)
	}
	if err := c.Delete(t.Context(), reg, "zefir/nudge", "latest", amd); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("delete with a stale digest: %v", err)
	}
	if err := c.Delete(t.Context(), reg, "zefir/nudge", "latest", d); err != nil {
		t.Fatal(err)
	}
	// Forgejo deletes the one version: v1 still points to the same index.
	if got := f.tagsOf("zefir/nudge"); len(got) != 2 || got["v1"] != d || got["latest"] != "" {
		t.Errorf("tags after delete = %v", got)
	}
	f.mu.Lock()
	reqs := slices.Clone(f.requests)
	f.mu.Unlock()
	if !slices.Contains(reqs, "DELETE /api/v1/packages/zefir/container/nudge/latest") {
		t.Errorf("no packages API delete in %v", reqs)
	}
	for _, r := range reqs {
		if strings.HasPrefix(r, "DELETE /v2/") {
			t.Errorf("Forgejo delete went through the v2 API: %s", r)
		}
	}
	bad := reg
	bad.Password = "wrong"
	err = c.Delete(t.Context(), bad, "zefir/nudge", "v1", d)
	checkSafe(t, err)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("delete with a wrong token: %v", err)
	}
}

func TestTimeout(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(func() { close(block); srv.Close() })
	u, _ := url.Parse(srv.URL)
	c := New(Options{Transport: srv.Client().Transport, Timeout: time.Millisecond})
	_, err := c.Tags(t.Context(), Registry{Address: u.Host}, "zefir/nudge")
	if !errors.Is(err, ErrUnreachable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

func TestDockerHubNames(t *testing.T) {
	t.Parallel()
	reg := Registry{Address: registries.DockerHub}
	if got := v2(reg, repoName(reg, "nginx")+"/tags/list").String(); got != "https://registry-1.docker.io/v2/library/nginx/tags/list" {
		t.Errorf("Docker Hub URL = %s", got)
	}
	if got := repoName(reg, "zefir/app"); got != "zefir/app" {
		t.Errorf("repoName = %s", got)
	}
	if got := repoName(Registry{Address: "ghcr.io"}, "app"); got != "app" {
		t.Errorf("repoName on ghcr.io = %s", got)
	}
}
