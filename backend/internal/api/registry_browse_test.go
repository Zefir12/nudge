package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/registryclient"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// fakeBrowser is a RegistryBrowser with canned answers; err fails every call.
type fakeBrowser struct {
	mu        sync.Mutex
	repos     []string
	tags      []string
	manifest  registryclient.Manifest
	shared    []string
	err       error
	deleteErr error
	calls     []string
	regs      []registryclient.Registry
}

func (f *fakeBrowser) record(reg registryclient.Registry, call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.regs = append(f.regs, reg)
	return f.err
}

func (f *fakeBrowser) Repositories(_ context.Context, reg registryclient.Registry) ([]string, error) {
	return f.repos, f.record(reg, "repositories")
}

func (f *fakeBrowser) Tags(_ context.Context, reg registryclient.Registry, repo string) ([]string, error) {
	return f.tags, f.record(reg, "tags "+repo)
}

func (f *fakeBrowser) Manifest(_ context.Context, reg registryclient.Registry, repo, tag string) (registryclient.Manifest, error) {
	return f.manifest, f.record(reg, "manifest "+repo+":"+tag)
}

func (f *fakeBrowser) SharedTags(_ context.Context, reg registryclient.Registry, repo, tag, digest string) ([]string, bool, error) {
	return f.shared, true, f.record(reg, "shared "+repo+":"+tag+"@"+digest)
}

func (f *fakeBrowser) Delete(_ context.Context, reg registryclient.Registry, repo, tag, digest string) error {
	if err := f.record(reg, "delete "+repo+":"+tag+"@"+digest); err != nil {
		return err
	}
	return f.deleteErr
}

type browseTest struct {
	h      http.Handler
	cookie *http.Cookie
	fake   *fakeBrowser
}

func newBrowseTest(t *testing.T, admin bool) *browseTest {
	t.Helper()
	fake := &fakeBrowser{}
	h, svc, _ := newTestHandlerOpts(t, func(environments.Environment) (docker.Client, error) {
		return &dockertest.Fake{}, nil
	}, func(o *Options) {
		o.RegistryClient = fake
		o.Admin = func(auth.User) bool { return admin }
	})
	if _, err := svc.CreateFirstUser(t.Context(), "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookieOf(t, do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds}))
	rec := do(t, h, request{method: "POST", path: "/api/v1/registries", cookie: cookie,
		body: `{"name":"Forge","address":"git.example.com","username":"bot","password":"tok","kind":"forgejo","owners":["zefir"]}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create registry: %d %s", rec.Code, rec.Body)
	}
	return &browseTest{h: h, cookie: cookie, fake: fake}
}

func (bt *browseTest) call(t *testing.T, method, path string) (int, string) {
	t.Helper()
	rec := do(t, bt.h, request{method: method, path: path, cookie: bt.cookie})
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

func TestRegistryBrowse(t *testing.T) {
	t.Parallel()
	many := make([]string, 150)
	for i := range many {
		many[i] = fmt.Sprintf("zefir/app%03d", i)
	}
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		method   string
		path     string
		setup    func(f *fakeBrowser)
		wantCode int
		// wantBody is the exact body, or with a leading "~" a part of it.
		wantBody  string
		wantCalls []string
	}{
		{name: "repositories", path: "/api/v1/registries/1/repositories",
			setup:    func(f *fakeBrowser) { f.repos = []string{"zefir/tools", "zefir/nudge"} },
			wantCode: 200, wantBody: `{"repositories":[{"name":"zefir/nudge"},{"name":"zefir/tools"}]}`},
		{name: "repositories filtered", path: "/api/v1/registries/1/repositories?q=TOO",
			setup:    func(f *fakeBrowser) { f.repos = []string{"zefir/tools", "zefir/nudge"} },
			wantCode: 200, wantBody: `{"repositories":[{"name":"zefir/tools"}]}`},
		{name: "repositories first page", path: "/api/v1/registries/1/repositories",
			setup:    func(f *fakeBrowser) { f.repos = many },
			wantCode: 200, wantBody: `~{"name":"zefir/app099"}],"next":"zefir/app099"}`},
		{name: "repositories next page", path: "/api/v1/registries/1/repositories?cursor=zefir/app099",
			setup:    func(f *fakeBrowser) { f.repos = many },
			wantCode: 200, wantBody: `~{"repositories":[{"name":"zefir/app100"},`},
		{name: "repositories without a catalog", path: "/api/v1/registries/1/repositories",
			setup:    func(f *fakeBrowser) { f.err = fmt.Errorf("%w: catalog answered 404", registryclient.ErrNoCatalog) },
			wantCode: 409, wantBody: `{"error":"registry doesn't list repositories; open one by name"}`},
		{name: "unknown registry", path: "/api/v1/registries/9/repositories", wantCode: 404, wantBody: `{"error":"registry not found"}`},
		{name: "tags", path: "/api/v1/registries/1/tags?repository=zefir/nudge",
			setup:    func(f *fakeBrowser) { f.tags = []string{"v1", "latest"} },
			wantCode: 200, wantBody: `[{"tag":"latest"},{"tag":"v1"}]`, wantCalls: []string{"tags zefir/nudge"}},
		{name: "tags none", path: "/api/v1/registries/1/tags?repository=zefir/nudge", wantCode: 200, wantBody: `[]`},
		{name: "tags of an unknown repository", path: "/api/v1/registries/1/tags?repository=zefir/nope",
			setup:    func(f *fakeBrowser) { f.err = registryclient.ErrNotFound },
			wantCode: 404, wantBody: `{"error":"repository or tag not found"}`},
		{name: "tags refused", path: "/api/v1/registries/1/tags?repository=zefir/nudge",
			setup: func(f *fakeBrowser) {
				f.err = fmt.Errorf("%w: token server answered 401", registryclient.ErrUnauthorized)
			},
			wantCode: 502, wantBody: `{"error":"registry refused the credentials"}`},
		{name: "tags unreachable", path: "/api/v1/registries/1/tags?repository=zefir/nudge",
			setup:    func(f *fakeBrowser) { f.err = errors.New("dial tcp 10.0.0.1:443: secret detail") },
			wantCode: 502, wantBody: `{"error":"registry unreachable"}`},
		{name: "repository with dots", path: "/api/v1/registries/1/tags?repository=../etc", wantCode: 400, wantBody: "~repository must be"},
		{name: "repository with an encoded slash", path: "/api/v1/registries/1/tags?repository=zefir%252Fnudge", wantCode: 400},
		{name: "repository missing", path: "/api/v1/registries/1/tags", wantCode: 400},
		{name: "manifest", path: "/api/v1/registries/1/manifest?repository=zefir/nudge&tag=latest",
			setup: func(f *fakeBrowser) {
				f.manifest = registryclient.Manifest{Digest: testDigest, MediaType: registryclient.MediaDockerV2, Size: 1234, Created: created}
				f.shared = []string{"v1"}
			},
			wantCode: 200,
			wantBody: `{"repository":"zefir/nudge","tag":"latest","digest":"` + testDigest + `","media_type":"` + registryclient.MediaDockerV2 +
				`","size":1234,"created":"2026-10-01T12:00:00Z","platforms":[],"shared_tags":["v1"],"shared_tags_complete":true}`,
			wantCalls: []string{"manifest zefir/nudge:latest", "shared zefir/nudge:latest@" + testDigest}},
		{name: "manifest of an index", path: "/api/v1/registries/1/manifest?repository=zefir/nudge&tag=multi",
			setup: func(f *fakeBrowser) {
				f.manifest = registryclient.Manifest{Digest: testDigest, MediaType: registryclient.MediaOCIIndex,
					Platforms: []registryclient.Platform{{OS: "linux", Architecture: "arm64", Variant: "v8", Digest: testDigest, Size: 9}}}
			},
			wantCode: 200, wantBody: `~"platforms":[{"os":"linux","architecture":"arm64","variant":"v8","digest":"` + testDigest + `","size":9}],"shared_tags":[]`},
		{name: "manifest bad tag", path: "/api/v1/registries/1/manifest?repository=zefir/nudge&tag=sha256:abc", wantCode: 400, wantBody: "~tag must be"},
		{name: "delete", method: "DELETE", path: "/api/v1/registries/1/tags?repository=zefir/nudge&tag=latest&digest=" + testDigest,
			wantCode: 204, wantCalls: []string{"delete zefir/nudge:latest@" + testDigest}},
		{name: "delete without digest", method: "DELETE", path: "/api/v1/registries/1/tags?repository=zefir/nudge&tag=latest",
			wantCode: 400, wantBody: "~digest must be"},
		{name: "delete with a moved tag", method: "DELETE", path: "/api/v1/registries/1/tags?repository=zefir/nudge&tag=latest&digest=" + testDigest,
			setup:    func(f *fakeBrowser) { f.deleteErr = registryclient.ErrDigestMismatch },
			wantCode: 409, wantBody: `{"error":"the tag points to another digest now"}`},
		{name: "delete refused by the registry", method: "DELETE", path: "/api/v1/registries/1/tags?repository=zefir/nudge&tag=latest&digest=" + testDigest,
			setup: func(f *fakeBrowser) {
				f.deleteErr = fmt.Errorf("%w: delete answered 405", registryclient.ErrDeleteUnsupported)
			},
			wantCode: 409, wantBody: `{"error":"registry doesn't allow deletes"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bt := newBrowseTest(t, true)
			if tt.setup != nil {
				tt.setup(bt.fake)
			}
			method := tt.method
			if method == "" {
				method = "GET"
			}
			code, body := bt.call(t, method, tt.path)
			if code != tt.wantCode {
				t.Fatalf("code = %d, want %d (%s)", code, tt.wantCode, body)
			}
			if part, ok := strings.CutPrefix(tt.wantBody, "~"); ok {
				if !strings.Contains(body, part) {
					t.Errorf("body = %s, want it to contain %s", body, part)
				}
			} else if tt.wantBody != "" && body != tt.wantBody {
				t.Errorf("body = %s, want %s", body, tt.wantBody)
			}
			if strings.Contains(body, "secret") || strings.Contains(body, `"tok"`) {
				t.Errorf("body leaks detail: %s", body)
			}
			bt.fake.mu.Lock()
			defer bt.fake.mu.Unlock()
			if tt.wantCalls != nil && fmt.Sprint(bt.fake.calls) != fmt.Sprint(tt.wantCalls) {
				t.Errorf("calls = %v, want %v", bt.fake.calls, tt.wantCalls)
			}
			for _, reg := range bt.fake.regs {
				if reg.Address != "git.example.com" || reg.Password != "tok" || reg.Username != "bot" ||
					reg.Kind != "forgejo" || fmt.Sprint(reg.Owners) != "[zefir]" {
					t.Errorf("client got registry %+v", reg)
				}
			}
		})
	}
}

func TestRegistryBrowsePermissions(t *testing.T) {
	t.Parallel()
	paths := []struct{ method, path string }{
		{"GET", "/api/v1/registries/1/repositories"},
		{"GET", "/api/v1/registries/1/tags?repository=zefir/nudge"},
		{"GET", "/api/v1/registries/1/manifest?repository=zefir/nudge&tag=latest"},
		{"DELETE", "/api/v1/registries/1/tags?repository=zefir/nudge&tag=latest&digest=" + testDigest},
	}
	t.Run("no session", func(t *testing.T) {
		t.Parallel()
		bt := newBrowseTest(t, true)
		for _, p := range paths {
			if rec := do(t, bt.h, request{method: p.method, path: p.path}); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without session: %d", p.method, p.path, rec.Code)
			}
		}
		if len(bt.fake.calls) != 0 {
			t.Errorf("registry called without a session: %v", bt.fake.calls)
		}
	})
	t.Run("not an admin", func(t *testing.T) {
		t.Parallel()
		bt := newBrowseTest(t, false)
		bt.fake.manifest = registryclient.Manifest{Digest: testDigest, MediaType: registryclient.MediaDockerV2}
		for _, p := range paths {
			want := http.StatusOK
			if p.method == "DELETE" {
				want = http.StatusForbidden
			}
			if code, body := bt.call(t, p.method, p.path); code != want {
				t.Errorf("%s %s as a user: %d %s, want %d", p.method, p.path, code, body, want)
			}
		}
		for _, c := range bt.fake.calls {
			if strings.HasPrefix(c, "delete") {
				t.Errorf("non-admin reached the registry: %s", c)
			}
		}
	})
}

func TestRegistryTagDeleteAudited(t *testing.T) {
	t.Parallel()
	bt := newBrowseTest(t, true)
	if code, body := bt.call(t, "DELETE", "/api/v1/registries/1/tags?repository=zefir/nudge&tag=v1&digest="+testDigest); code != 204 {
		t.Fatalf("delete: %d %s", code, body)
	}
	bt.fake.deleteErr = registryclient.ErrDigestMismatch
	if code, _ := bt.call(t, "DELETE", "/api/v1/registries/1/tags?repository=zefir/nudge&tag=v2&digest="+testDigest); code != 409 {
		t.Fatalf("delete with a stale digest: %d", code)
	}
	got := auditEntries(t, bt.h, bt.cookie, "")
	var deletes []string
	for _, e := range got.Entries {
		if e.Action == "DELETE /api/v1/registries/{id}/tags" {
			deletes = append(deletes, fmt.Sprintf("%s %d", e.Target, e.Status))
		}
	}
	want := []string{"git.example.com/zefir/nudge:v2 409", "git.example.com/zefir/nudge:v1 204"}
	if fmt.Sprint(deletes) != fmt.Sprint(want) {
		b, _ := json.Marshal(got.Entries)
		t.Errorf("audited deletes = %v, want %v (entries %s)", deletes, want, b)
	}
}
