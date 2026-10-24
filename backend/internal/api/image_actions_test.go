package api

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func TestImageActions(t *testing.T) {
	t.Parallel()
	report := image.PruneReport{
		ImagesDeleted:  []image.DeleteResponse{{Untagged: "old:1"}, {Deleted: "sha256:a"}, {Deleted: "sha256:b"}},
		SpaceReclaimed: 4096,
	}
	tests := []struct {
		name     string
		method   string
		path     string
		fake     *dockertest.Fake
		wantCode int
		wantErr  string
		wantBody string
		want     []dockertest.Action
	}{
		{name: "remove", method: "DELETE", path: "/images/nginx:1.29", wantCode: 204,
			want: []dockertest.Action{{Name: "image-remove", ID: "nginx:1.29", Options: client.ImageRemoveOptions{PruneChildren: true}}}},
		{name: "remove by path with slashes", method: "DELETE", path: "/images/ghcr.io%2Forg%2Fapp:1.2?force=true", wantCode: 204,
			want: []dockertest.Action{{Name: "image-remove", ID: "ghcr.io/org/app:1.2", Options: client.ImageRemoveOptions{Force: true, PruneChildren: true}}}},
		{name: "remove unknown", method: "DELETE", path: "/images/nope", wantCode: 404, wantErr: "image not found"},
		{name: "remove invalid id", method: "DELETE", path: "/images/..%2F..", wantCode: 404, wantErr: "image not found"},
		{name: "remove invalid force", method: "DELETE", path: "/images/nginx:1.29?force=maybe", wantCode: 400, wantErr: "force must be true or false"},
		{name: "remove in use", method: "DELETE", path: "/images/nginx:1.29",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: fmt.Errorf("secret-detail: %w", cerrdefs.ErrConflict)}},
			wantCode: 409, wantErr: "the image is in use by a container or has several tags: remove it with force=true (images of running containers can't be removed)"},
		{name: "remove offline", method: "DELETE", path: "/images/nginx:1.29",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: &net.OpError{Op: "dial", Err: errors.New("refused")}}},
			wantCode: 503, wantErr: "the environment is offline"},

		{name: "prune dangling", method: "POST", path: "/images/prune",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{PruneReport: report}},
			wantCode: 200, wantBody: `{"deleted":["sha256:a","sha256:b"],"untagged":["old:1"],"space_reclaimed":4096}`,
			want: []dockertest.Action{{Name: "image-prune", Options: client.ImagePruneOptions{Filters: client.Filters{}.Add("dangling", "true")}}}},
		{name: "prune all unused", method: "POST", path: "/images/prune?all=true", wantCode: 200,
			wantBody: `{"deleted":[],"untagged":[],"space_reclaimed":0}`,
			want:     []dockertest.Action{{Name: "image-prune", Options: client.ImagePruneOptions{Filters: client.Filters{}.Add("dangling", "false")}}}},
		{name: "prune invalid all", method: "POST", path: "/images/prune?all=yes", wantCode: 400, wantErr: "all must be true or false"},
		{name: "prune already running", method: "POST", path: "/images/prune",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: cerrdefs.ErrConflict}},
			wantCode: 409, wantErr: "an image prune is already running"},
		{name: "prune daemon error", method: "POST", path: "/images/prune",
			fake:     &dockertest.Fake{Resources: dockertest.Resources{ActionErr: errors.New("secret-detail")}},
			wantCode: 502},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := tt.fake
			if fake == nil {
				fake = &dockertest.Fake{}
			}
			fake.ImageDetails = map[string]image.InspectResponse{"nginx:1.29": {}, "ghcr.io/org/app:1.2": {}}
			et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
			path := fmt.Sprintf("/api/v1/environments/%d%s", et.ids["local"], tt.path)
			rec := do(t, et.h, request{method: tt.method, path: path, cookie: et.cookie})
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
			switch {
			case tt.wantCode == http.StatusNoContent:
				if rec.Body.Len() != 0 {
					t.Errorf("body = %q, want none", rec.Body)
				}
			case tt.wantBody != "":
				if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
					t.Errorf("body = %s, want %s", got, tt.wantBody)
				}
			default:
				var body struct{ Error string }
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
					t.Fatalf("body = %s, want a JSON error", rec.Body)
				}
				if tt.wantErr != "" && body.Error != tt.wantErr {
					t.Errorf("error = %q, want %q", body.Error, tt.wantErr)
				}
				if strings.Contains(body.Error, "secret") {
					t.Errorf("error leaks daemon details: %s", rec.Body)
				}
			}
			if got := fake.Actions(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("actions = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPullImage(t *testing.T) {
	t.Parallel()
	progress := []jsonstream.Message{
		{Status: "Pulling from org/app", ID: "1.2"},
		{Status: "Downloading", ID: "l1", Progress: &jsonstream.Progress{Current: 10, Total: 100}},
		{Status: "Status: Downloaded newer image for ghcr.io/org/app:1.2"},
	}
	tests := []struct {
		name     string
		body     string
		fake     *dockertest.Fake
		wantCode int
		wantErr  string
		// wantLines are the stream's lines for a 200.
		wantLines []string
		wantPull  string
	}{
		{name: "progress", body: `{"image":"ghcr.io/org/app:1.2"}`,
			fake:     &dockertest.Fake{Resources: dockertest.Resources{PullMessages: progress}},
			wantCode: 200, wantPull: "ghcr.io/org/app:1.2",
			wantLines: []string{
				`{"id":"1.2","status":"Pulling from org/app"}`,
				`{"id":"l1","status":"Downloading","current":10,"total":100}`,
				`{"status":"Status: Downloaded newer image for ghcr.io/org/app:1.2"}`,
				`{"done":true}`,
			}},
		{name: "error in stream masks the password", body: `{"image":"ghcr.io/org/app:1.2"}`,
			fake: &dockertest.Fake{Resources: dockertest.Resources{PullMessages: []jsonstream.Message{
				{Status: "Pulling hunter2"},
				{Error: &jsonstream.Error{Message: "denied for hunter2"}},
				{Status: "never sent"},
			}}},
			wantCode: 200, wantPull: "ghcr.io/org/app:1.2",
			wantLines: []string{`{"status":"Pulling ********"}`, `{"error":"denied for ********"}`}},
		{name: "pinned digest", body: `{"image":"ghcr.io/org/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`,
			wantCode: 200, wantPull: "ghcr.io/org/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			wantLines: []string{`{"done":true}`}},
		{name: "not found", body: `{"image":"nope:latest"}`,
			fake:     &dockertest.Fake{Resources: dockertest.Resources{PullErr: fmt.Errorf("Error response from daemon: pull access denied for nope: %w", cerrdefs.ErrNotFound)}},
			wantCode: 404, wantErr: "pull failed: pull access denied for nope: not found"},
		{name: "unauthorized masks the password", body: `{"image":"ghcr.io/org/app"}`,
			fake:     &dockertest.Fake{Resources: dockertest.Resources{PullErr: fmt.Errorf("bad hunter2: %w", cerrdefs.ErrUnauthenticated)}},
			wantCode: 403, wantErr: "pull failed: bad ********: unauthorized"},
		{name: "offline", body: `{"image":"nginx"}`,
			fake:     &dockertest.Fake{Resources: dockertest.Resources{PullErr: &net.OpError{Op: "dial", Err: errors.New("refused")}}},
			wantCode: 503, wantErr: "the environment is offline"},
		{name: "empty image", body: `{"image":""}`, wantCode: 400},
		{name: "invalid image", body: `{"image":"nginx; rm -rf"}`, wantCode: 400},
		{name: "dot dot", body: `{"image":"../../etc"}`, wantCode: 400},
		{name: "at without digest", body: `{"image":"user:pw@evil.com/img"}`, wantCode: 400},
		{name: "at before path", body: `{"image":"docker.io@x/y"}`, wantCode: 400},
		{name: "malformed body", body: `{`, wantCode: 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := tt.fake
			if fake == nil {
				fake = &dockertest.Fake{}
			}
			et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
			rec := do(t, et.h, request{method: "POST", path: "/api/v1/registries", cookie: et.cookie,
				body: `{"name":"GitHub","address":"ghcr.io","username":"bot","password":"hunter2"}`})
			if rec.Code != http.StatusCreated {
				t.Fatalf("create registry: %d %s", rec.Code, rec.Body)
			}
			path := fmt.Sprintf("/api/v1/environments/%d/images/pull", et.ids["local"])
			rec = do(t, et.h, request{method: "POST", path: path, cookie: et.cookie, body: tt.body})
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
			if tt.wantCode != http.StatusOK {
				var body struct{ Error string }
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
					t.Fatalf("body = %s, want a JSON error", rec.Body)
				}
				if tt.wantErr != "" && body.Error != tt.wantErr {
					t.Errorf("error = %q, want %q", body.Error, tt.wantErr)
				}
				return
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/x-ndjson" {
				t.Errorf("Content-Type = %q", ct)
			}
			var lines []string
			sc := bufio.NewScanner(rec.Body)
			for sc.Scan() {
				lines = append(lines, sc.Text())
			}
			if !reflect.DeepEqual(lines, tt.wantLines) {
				t.Errorf("lines =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(tt.wantLines, "\n"))
			}
			actions := fake.Actions()
			if len(actions) != 1 || actions[0].ID != tt.wantPull {
				t.Fatalf("actions = %+v", actions)
			}
			auth := decodeAuth(t, actions[0].Options.(client.ImagePullOptions).RegistryAuth)
			if auth.Username != "bot" || auth.Password != "hunter2" || auth.ServerAddress != "ghcr.io" {
				t.Errorf("registry auth = %+v", auth)
			}
		})
	}
}

func TestPullImageWithoutCredentials(t *testing.T) {
	t.Parallel()
	fake := &dockertest.Fake{}
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
	path := fmt.Sprintf("/api/v1/environments/%d/images/pull", et.ids["local"])
	rec := do(t, et.h, request{method: "POST", path: path, cookie: et.cookie, body: `{"image":"nginx:1.29"}`})
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"done":true}` {
		t.Fatalf("pull = %d %s", rec.Code, rec.Body)
	}
	want := []dockertest.Action{{Name: "image-pull", ID: "nginx:1.29", Options: client.ImagePullOptions{}}}
	if got := fake.Actions(); !reflect.DeepEqual(got, want) {
		t.Errorf("actions = %+v, want %+v", got, want)
	}
}

func TestImageActionsNeedSession(t *testing.T) {
	t.Parallel()
	fake := &dockertest.Fake{Resources: dockertest.Resources{ImageDetails: map[string]image.InspectResponse{"nginx": {}}}}
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
	base := fmt.Sprintf("/api/v1/environments/%d/images", et.ids["local"])
	for _, req := range []request{
		{method: "POST", path: base + "/pull", body: `{"image":"nginx"}`},
		{method: "POST", path: base + "/prune"},
		{method: "DELETE", path: base + "/nginx"},
	} {
		if rec := do(t, et.h, req); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without session: code = %d, want 401", req.method, req.path, rec.Code)
		}
	}
	if got := fake.Actions(); len(got) != 0 {
		t.Errorf("actions = %+v, want none", got)
	}
}

func decodeAuth(t *testing.T, s string) registry.AuthConfig {
	t.Helper()
	raw, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode registry auth %q: %v", s, err)
	}
	var a registry.AuthConfig
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	return a
}
