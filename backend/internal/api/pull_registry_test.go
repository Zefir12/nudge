package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestPullRegistry(t *testing.T) {
	t.Parallel()
	tests := []struct{ ref, want string }{
		{"nginx", "docker.io"},
		{"nginx:1.29", "docker.io"},
		{"library/nginx@sha256:" + strings.Repeat("a", 64), "docker.io"},
		{"me/app:1", "docker.io"},
		{"docker.io/library/nginx", "docker.io"},
		{"index.docker.io/library/nginx", "docker.io"},
		{"GHCR.IO/me/app", "ghcr.io"},
		{"ghcr.io/me/app:1", "ghcr.io"},
		{"10.0.0.5:5000/x", "10.0.0.5:5000"},
		{"10.0.0.5/x", "10.0.0.5"},
		{"intranet:80/x", "intranet:80"},
		{"localhost/x", "localhost"},
		{"Intranet/x", "intranet"},
	}
	for _, tt := range tests {
		if got := pullRegistry(tt.ref); got != tt.want {
			t.Errorf("pullRegistry(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestRegistrySetCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		hosts []string
		ref   string
		ok    bool
	}{
		{"default Docker Hub", nil, "nginx:1", true},
		{"default ghcr", nil, "ghcr.io/me/app", true},
		{"default internal host", nil, "10.0.0.5:5000/x", false},
		{"default localhost", nil, "localhost:5000/x", false},
		{"default upper-case short host", nil, "Intranet/x", false},
		{"default Hub on another port", nil, "docker.io:8443/x", false},
		{"listed", []string{"registry.example.net:5000"}, "registry.example.net:5000/x", true},
		{"Hub not listed", []string{"registry.example.net:5000"}, "nginx", false},
		{"Hub alias listed", []string{"index.docker.io"}, "nginx", true},
		{"any", []string{"*"}, "10.0.0.5:5000/x", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := newRegistrySet(tt.hosts).check(tt.ref)
			if (err == nil) != tt.ok {
				t.Fatalf("check(%q) = %v, want ok %v", tt.ref, err, tt.ok)
			}
			var se *statusError
			if err != nil && (!errors.As(err, &se) || se.code != http.StatusForbidden) {
				t.Errorf("err = %#v, want a 403", err)
			}
		})
	}
}

// A user's pull from a registry outside the list never reaches the daemon.
func TestUserPullRegistryRefused(t *testing.T) {
	t.Parallel()
	ot := newTenantTestOpts(t, func(o *Options) { o.UserRegistries = []string{"ghcr.io"} })
	path := ot.path(ot.local, "/user-containers")
	for _, img := range []string{"nginx:1", "10.0.0.5:5000/x:1"} {
		before := len(ot.fake.Actions())
		rec := do(t, ot.h, request{method: "POST", path: path, cookie: ot.bob, body: `{` + tenantBase + `,"image":"` + img + `"}`})
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not allowed for users") {
			t.Errorf("%s: = %d %s, want 403", img, rec.Code, rec.Body)
		}
		for _, a := range ot.fake.Actions()[before:] {
			if a.Name == "image-pull" {
				t.Errorf("%s: pulled", img)
			}
		}
	}
	rec := do(t, ot.h, request{method: "POST", path: path, cookie: ot.bob, body: `{` + tenantBase + `,"image":"ghcr.io/me/web:1"}`})
	if rec.Code == http.StatusForbidden {
		t.Errorf("listed registry = %d %s", rec.Code, rec.Body)
	}
}
