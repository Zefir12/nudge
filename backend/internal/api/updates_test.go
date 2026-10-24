package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	godigest "github.com/opencontainers/go-digest"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
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
