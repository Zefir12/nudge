package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	godigest "github.com/opencontainers/go-digest"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

func TestUpdates(t *testing.T) {
	t.Parallel()
	local := &dockertest.Fake{Resources: dockertest.Resources{
		Containers: []container.Summary{
			{ID: "a", Image: "nginx:1.29", ImageID: "sha256:n1"},
			{ID: "b", Image: "ghcr.io/o/app:v1", ImageID: "sha256:a1"},
		},
		ImageDetails: map[string]image.InspectResponse{
			"sha256:n1": {RepoDigests: []string{"nginx@sha256:old"}},
			"sha256:a1": {RepoDigests: []string{"ghcr.io/o/app@sha256:app"}},
		},
		Distribution: map[string]godigest.Digest{"nginx:1.29": "sha256:new", "ghcr.io/o/app:v1": "sha256:app"},
	}}
	down := &dockertest.Fake{Resources: dockertest.Resources{ResourceErr: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}}
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": local, "down": down})
	path := func(env string) string { return fmt.Sprintf("/api/v1/environments/%d/updates", et.ids[env]) }

	rec := do(t, et.h, request{method: "GET", path: path("local"), cookie: et.cookie})
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("GET before a check = %d %s, want 200 []", rec.Code, rec.Body)
	}

	rec = do(t, et.h, request{method: "POST", path: path("local") + "/check", cookie: et.cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST check = %d %s", rec.Code, rec.Body)
	}
	var checked []updateJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &checked); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"nginx:1.29": "available", "ghcr.io/o/app:v1": "current"}
	if len(checked) != len(want) {
		t.Fatalf("check results = %+v, want %v", checked, want)
	}
	for _, u := range checked {
		if string(u.Status) != want[u.Image] {
			t.Errorf("%s: status %q, want %q", u.Image, u.Status, want[u.Image])
		}
	}

	rec = do(t, et.h, request{method: "GET", path: path("local"), cookie: et.cookie})
	var listed []updateJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(listed) != 2 || listed[1].Image != "nginx:1.29" || listed[1].RemoteDigest != "sha256:new" || listed[1].LocalDigest != "sha256:old" {
		t.Fatalf("GET after the check = %d %+v", rec.Code, listed)
	}
	if !strings.Contains(rec.Body.String(), `"checked_at":"`) {
		t.Errorf("GET body %s has no checked_at", rec.Body)
	}

	tests := []struct {
		name   string
		method string
		path   string
		cookie bool
		want   int
	}{
		{"offline environment", "POST", path("down") + "/check", true, http.StatusServiceUnavailable},
		{"unknown environment", "GET", "/api/v1/environments/999/updates", true, http.StatusNotFound},
		{"unknown environment check", "POST", "/api/v1/environments/999/updates/check", true, http.StatusNotFound},
		{"no session", "GET", path("local"), false, http.StatusUnauthorized},
		{"no session check", "POST", path("local") + "/check", false, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := request{method: tt.method, path: tt.path}
			if tt.cookie {
				req.cookie = et.cookie
			}
			if rec := do(t, et.h, req); rec.Code != tt.want {
				t.Errorf("%s %s = %d %s, want %d", tt.method, tt.path, rec.Code, rec.Body, tt.want)
			}
		})
	}
}

// history lists an environment's update history with query.
func history(t *testing.T, et envTest, env, query string) []updateHistoryJSON {
	t.Helper()
	rec := do(t, et.h, request{method: "GET", path: fmt.Sprintf("/api/v1/environments/%d/updates/history%s", et.ids[env], query), cookie: et.cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET history%s = %d %s", query, rec.Code, rec.Body)
	}
	var out []updateHistoryJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUpdateContainer(t *testing.T) {
	t.Parallel()
	pinned := "nginx@sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name string
		// ref is the image the container was created from; pulled the image ref names
		// after the pull.
		ref       string
		pulled    string
		fail      string
		code      int
		want      updateContainerResponse
		wantError string
		recreated bool
	}{
		{
			name: "newer image", ref: "nginx:1.29", pulled: "sha256:img2", code: http.StatusOK, recreated: true,
			want: updateContainerResponse{Updated: true, ID: "new-1", PreviousImageID: "sha256:img1", ImageID: "sha256:img2"},
		},
		{
			name: "already current", ref: "nginx:1.29", pulled: "sha256:img1", code: http.StatusOK,
			want: updateContainerResponse{ID: "abc123def4567890", PreviousImageID: "sha256:img1", ImageID: "sha256:img1"},
		},
		{name: "pinned to a digest", ref: pinned, code: http.StatusBadRequest, wantError: "by ID or digest"},
		{name: "image ID", ref: "sha256:" + strings.Repeat("b", 64), code: http.StatusBadRequest, wantError: "by ID or digest"},
		{
			name: "create fails", ref: "nginx:1.29", pulled: "sha256:img2", fail: "create", code: http.StatusBadRequest,
			wantError: "could not be created", recreated: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ct := oldWeb()
			ct.Config.Image = tt.ref
			images := map[string]image.InspectResponse{
				"sha256:img1": {ID: "sha256:img1", RepoDigests: []string{"nginx@sha256:old"}},
				"sha256:img2": {ID: "sha256:img2", RepoDigests: []string{"nginx@sha256:new"}},
			}
			if tt.pulled != "" {
				images[tt.ref] = images[tt.pulled]
			}
			fail := func(a dockertest.Action) error {
				if a.Name == tt.fail {
					return errors.New("Error response from daemon: port is already allocated")
				}
				return nil
			}
			et, fake, base := newRecreateTest(t, ct, images, fail)
			rec := do(t, et.h, request{method: "POST", path: base + "/update", cookie: et.cookie})
			if rec.Code != tt.code {
				t.Fatalf("POST update = %d %s, want %d", rec.Code, rec.Body, tt.code)
			}
			if tt.wantError != "" && !strings.Contains(rec.Body.String(), tt.wantError) {
				t.Errorf("body = %s, want it to say %q", rec.Body, tt.wantError)
			}
			if tt.code == http.StatusOK {
				var got updateContainerResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got != tt.want {
					t.Errorf("answer = %+v, want %+v", got, tt.want)
				}
			}
			// A recreate starts with the stop; a failed one ends with the old one's start.
			stopped := false
			for _, a := range fake.Actions() {
				stopped = stopped || a.Name == "stop"
			}
			if stopped != tt.recreated {
				t.Errorf("recreated = %v, want %v (actions %+v)", stopped, tt.recreated, fake.Actions())
			}
			if acts := fake.Actions(); tt.fail != "" && (len(acts) == 0 || acts[len(acts)-1].Name != "start" || acts[len(acts)-1].ID != ct.ID) {
				t.Errorf("actions = %+v, want the old container started again", acts)
			}

			hist := history(t, et, "local", "?kind=container&target=web")
			if !tt.want.Updated {
				if len(hist) != 0 {
					t.Errorf("history = %+v, want none", hist)
				}
			} else if len(hist) != 1 || hist[0].OldImageID != "sha256:img1" || hist[0].OldDigest != "sha256:old" ||
				hist[0].NewImageID != "sha256:img2" || hist[0].NewDigest != "sha256:new" || hist[0].Image != "nginx:1.29" ||
				hist[0].User != "admin" || hist[0].Kind != "container" {
				t.Errorf("history = %+v, want web's update from img1 to img2", hist)
			}
			entries := auditEntries(t, et.h, et.cookie, "")
			if len(entries.Entries) == 0 || entries.Entries[0].Action != "POST /api/v1/environments/{env}/containers/{id}/update" || entries.Entries[0].Status != tt.code {
				t.Errorf("audit = %+v, want the update with %d", entries.Entries, tt.code)
			}
		})
	}
}

func TestUpdateStack(t *testing.T) {
	t.Parallel()
	exit := func(n int) *int { return &n }
	for _, code := range []int{0, 1} {
		t.Run(fmt.Sprintf("exit %d", code), func(t *testing.T) {
			t.Parallel()
			st := newStackTest(t)
			label := func(service string) map[string]string {
				return map[string]string{stacks.ProjectLabel: "web", stacks.ServiceLabel: service}
			}
			st.fake.Containers = []container.Summary{
				{ID: "a1", Image: "ghcr.io/o/app:v1", ImageID: "sha256:a1", Labels: label("app")},
				{ID: "d1", Image: "postgres:17", ImageID: "sha256:d1", Labels: label("db")},
			}
			st.fake.ImageDetails = map[string]image.InspectResponse{
				"sha256:a1": {RepoDigests: []string{"ghcr.io/o/app@sha256:old"}},
				"sha256:a2": {RepoDigests: []string{"ghcr.io/o/app@sha256:new"}},
			}
			st.runner.code = code
			st.runner.onRun = func() {
				st.fake.Containers = []container.Summary{
					{ID: "a2", Image: "ghcr.io/o/app:v1", ImageID: "sha256:a2", Labels: label("app")},
					{ID: "d1", Image: "postgres:17", ImageID: "sha256:d1", Labels: label("db")},
					{ID: "n1", Image: "redis:8", ImageID: "sha256:n1", Labels: label("cache")},
				}
			}
			if c, body := st.do(t, "POST", "local", "", `{"name":"web","compose":"services: {}\n"}`); c != http.StatusCreated {
				t.Fatalf("create = %d %s", c, body)
			}
			c, body := st.do(t, "POST", "local", "/web/update", "")
			if c != http.StatusOK {
				t.Fatalf("POST update = %d %s", c, body)
			}
			if got := stackEvents(t, body); !reflect.DeepEqual(got, []stackEvent{{ExitCode: exit(code)}}) {
				t.Errorf("events = %+v", got)
			}
			if !reflect.DeepEqual(st.runner.calls, []string{"up web pull=true"}) {
				t.Errorf("runner calls = %q", st.runner.calls)
			}
			// A failed run records what it replaced all the same.
			hist := history(t, st.envTest, "local", "?kind=stack&target=web")
			if len(hist) != 1 || hist[0].Service != "app" || hist[0].Image != "ghcr.io/o/app:v1" ||
				hist[0].OldImageID != "sha256:a1" || hist[0].OldDigest != "sha256:old" ||
				hist[0].NewImageID != "sha256:a2" || hist[0].NewDigest != "sha256:new" || hist[0].Target != "web" {
				t.Errorf("history = %+v, want only app's update", hist)
			}
		})
	}
}

func TestUpdateHistoryRequests(t *testing.T) {
	t.Parallel()
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": {}})
	base := fmt.Sprintf("/api/v1/environments/%d/updates/history", et.ids["local"])
	tests := []struct {
		name   string
		path   string
		cookie bool
		want   int
	}{
		{"empty", base, true, http.StatusOK},
		{"filtered", base + "?kind=stack&target=web", true, http.StatusOK},
		{"bad kind", base + "?kind=image", true, http.StatusBadRequest},
		{"unknown environment", "/api/v1/environments/999/updates/history", true, http.StatusNotFound},
		{"no session", base, false, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := request{method: "GET", path: tt.path}
			if tt.cookie {
				req.cookie = et.cookie
			}
			if rec := do(t, et.h, req); rec.Code != tt.want {
				t.Errorf("GET %s = %d %s, want %d", tt.path, rec.Code, rec.Body, tt.want)
			}
		})
	}
	for _, path := range []string{"/containers/x/update", "/stacks/web/update"} {
		p := fmt.Sprintf("/api/v1/environments/%d%s", et.ids["local"], path)
		if rec := do(t, et.h, request{method: "POST", path: p}); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without session = %d, want 401", path, rec.Code)
		}
	}
}
