package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func newResourceTest(t *testing.T) envTest {
	t.Helper()
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	web := container.Summary{
		ID: "abc123", Names: []string{"/web"}, Image: "nginx:1.29", ImageID: "sha256:img1",
		Command: "nginx -g 'daemon off;'", Created: created.Unix(), State: container.StateRunning,
		Status: "Up 2 hours", Health: &container.HealthSummary{Status: container.Healthy},
		Ports:           []container.PortSummary{{IP: netip.MustParseAddr("0.0.0.0"), PrivatePort: 80, PublicPort: 8080, Type: "tcp"}},
		NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{"front": {}, "back": {}}},
		Mounts:          []container.MountPoint{{Type: "volume", Name: "data", Source: "/var/lib/docker/volumes/data/_data", Destination: "/data", RW: true}},
	}
	old := container.Summary{ID: "def456", Names: []string{"/old"}, Image: "busybox", State: container.StateExited, Status: "Exited (0) 3 days ago"}
	fake := &dockertest.Fake{Resources: dockertest.Resources{
		Containers: []container.Summary{web, old},
		ContainerDetails: map[string]container.InspectResponse{"web": {
			ID: "abc123", Name: "/web", Image: "sha256:img1", Created: "2026-10-01T12:00:00.123Z",
			Path: "nginx", Args: []string{"-g", "daemon off;"}, RestartCount: 1,
			State: &container.State{Status: container.StateRunning, Running: true, Pid: 42, StartedAt: "2026-10-01T12:00:01Z", FinishedAt: "0001-01-01T00:00:00Z"},
			Config: &container.Config{
				Image: "nginx:1.29", Env: []string{"A=1"}, ExposedPorts: network.PortSet{network.MustParsePort("80/tcp"): {}, network.MustParsePort("443/tcp"): {}},
			},
			HostConfig: &container.HostConfig{RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}, NetworkMode: "bridge"},
			NetworkSettings: &container.NetworkSettings{
				Ports: network.PortMap{
					network.MustParsePort("80/tcp"):  {{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: "8080"}},
					network.MustParsePort("443/tcp"): nil,
				},
				Networks: map[string]*network.EndpointSettings{"front": {NetworkID: "n1", IPAddress: netip.MustParseAddr("172.18.0.2"), Aliases: []string{"web"}}},
			},
		}},
		Images: []image.Summary{
			{ID: "sha256:img1", RepoTags: []string{"nginx:1.29"}, RepoDigests: []string{"nginx@sha256:d1"}, Created: created.Unix(), Size: 1000},
			{ID: "sha256:img2", RepoTags: []string{"<none>:<none>"}, RepoDigests: []string{"<none>@<none>"}},
		},
		ImageDetails: map[string]image.InspectResponse{"ghcr.io/org/app:1.2": {
			ID: "sha256:img3", RepoTags: []string{"ghcr.io/org/app:1.2"}, Created: "2026-09-01T00:00:00Z", Os: "linux", Architecture: "amd64",
			Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{
				Cmd: []string{"app"}, ExposedPorts: map[string]struct{}{"8080/tcp": {}}, Volumes: map[string]struct{}{"/data": {}},
			}},
			RootFS: image.RootFS{Layers: []string{"sha256:l1"}},
		}},
		Volumes: []volume.Volume{
			{Name: "media", Driver: "local", Scope: "local", Options: map[string]string{"type": "cifs", "o": "username=u,password=secret-pw"}},
			{Name: "data", Driver: "local", Scope: "local", CreatedAt: "2026-10-01T12:00:00Z", Labels: map[string]string{"app": "web"}},
		},
		Networks: []network.Summary{
			{Network: network.Network{ID: "n2", Name: "front", Driver: "bridge", Scope: "local"}},
			{Network: network.Network{ID: "n1", Name: "back", Driver: "bridge", Scope: "local", Created: created, IPAM: network.IPAM{
				Config: []network.IPAMConfig{{Subnet: netip.MustParsePrefix("172.18.0.0/16"), Gateway: netip.MustParseAddr("172.18.0.1")}},
			}}},
		},
		NetworkDetails: map[string]network.Inspect{"front": {
			Network:    network.Network{ID: "n2", Name: "front", Driver: "bridge", Options: map[string]string{"com.docker.network.bridge.name": "br-front"}},
			Containers: map[string]network.EndpointResource{"abc123": {Name: "web", IPv4Address: netip.MustParsePrefix("172.18.0.2/16")}},
		}},
	}}
	return newEnvTest(t, map[string]*dockertest.Fake{
		"local":  fake,
		"down":   {Resources: dockertest.Resources{ResourceErr: &net.OpError{Op: "dial", Err: errors.New("connection refused: secret-detail")}}},
		"broken": {Resources: dockertest.Resources{ResourceErr: errors.New("Error response from daemon: secret-detail")}},
	})
}

func TestResourceEndpoints(t *testing.T) {
	t.Parallel()
	et := newResourceTest(t)
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		path string
		got  any
		want any
	}{
		{
			name: "all containers",
			path: "/containers",
			got:  &[]containerSummaryResponse{},
			want: &[]containerSummaryResponse{
				{
					ID: "abc123", Name: "web", Image: "nginx:1.29", ImageID: "sha256:img1", Command: "nginx -g 'daemon off;'",
					CreatedAt: &created, State: "running", Status: "Up 2 hours", Health: "healthy",
					Ports:  []portResponse{{PrivatePort: 80, Protocol: "tcp", HostIP: "0.0.0.0", PublicPort: 8080}},
					Labels: map[string]string{}, Networks: []string{"back", "front"},
					Mounts: []mountResponse{{Type: "volume", Name: "data", Source: "/var/lib/docker/volumes/data/_data", Destination: "/data", ReadWrite: true}},
				},
				{
					ID: "def456", Name: "old", Image: "busybox", State: "exited", Status: "Exited (0) 3 days ago",
					Ports: []portResponse{}, Labels: map[string]string{}, Networks: []string{}, Mounts: []mountResponse{},
				},
			},
		},
		{
			name: "exited containers",
			path: "/containers?state=exited",
			got:  &[]containerSummaryResponse{},
			want: &[]containerSummaryResponse{{
				ID: "def456", Name: "old", Image: "busybox", State: "exited", Status: "Exited (0) 3 days ago",
				Ports: []portResponse{}, Labels: map[string]string{}, Networks: []string{}, Mounts: []mountResponse{},
			}},
		},
		{
			name: "container",
			path: "/containers/web",
			got:  &containerDetailResponse{},
			want: &containerDetailResponse{
				ID: "abc123", Name: "web", Image: "nginx:1.29", ImageID: "sha256:img1",
				CreatedAt: ptr(created.Add(123 * time.Millisecond)), RestartCount: 1, Path: "nginx", Args: []string{"-g", "daemon off;"},
				State: containerStateResponse{Status: "running", Running: true, Pid: 42, StartedAt: ptr(created.Add(time.Second))},
				Config: containerConfigResponse{
					Env: []string{"A=1"}, Cmd: []string{}, Entrypoint: []string{}, ExposedPorts: []string{"443/tcp", "80/tcp"}, Labels: map[string]string{},
				},
				RestartPolicy: restartPolicyResponse{Name: "unless-stopped"}, NetworkMode: "bridge",
				Ports: []portResponse{
					{PrivatePort: 80, Protocol: "tcp", HostIP: "0.0.0.0", PublicPort: 8080},
					{PrivatePort: 443, Protocol: "tcp"},
				},
				Mounts:   []mountResponse{},
				Networks: []containerNetworkResponse{{Name: "front", NetworkID: "n1", IPAddress: "172.18.0.2", Aliases: []string{"web"}}},
			},
		},
		{
			name: "images",
			path: "/images",
			got:  &[]imageSummaryResponse{},
			want: &[]imageSummaryResponse{
				{ID: "sha256:img1", Tags: []string{"nginx:1.29"}, Digests: []string{"nginx@sha256:d1"}, CreatedAt: &created, Size: 1000, Labels: map[string]string{}},
				{ID: "sha256:img2", Tags: []string{}, Digests: []string{}, Labels: map[string]string{}, Dangling: true},
			},
		},
		{
			name: "image by reference",
			path: "/images/ghcr.io%2Forg%2Fapp:1.2",
			got:  &imageDetailResponse{},
			want: &imageDetailResponse{
				ID: "sha256:img3", Tags: []string{"ghcr.io/org/app:1.2"}, Digests: []string{}, CreatedAt: ptr(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)),
				Architecture: "amd64", OS: "linux", Layers: []string{"sha256:l1"},
				Config: imageConfigResponse{
					Env: []string{}, Cmd: []string{"app"}, Entrypoint: []string{}, ExposedPorts: []string{"8080/tcp"}, Volumes: []string{"/data"}, Labels: map[string]string{},
				},
			},
		},
		{
			name: "volumes sorted by name",
			path: "/volumes",
			got:  &[]volumeResponse{},
			want: &[]volumeResponse{
				{Name: "data", Driver: "local", Scope: "local", CreatedAt: &created, Labels: map[string]string{"app": "web"}},
				{Name: "media", Driver: "local", Scope: "local", Labels: map[string]string{}},
			},
		},
		{
			name: "volume",
			path: "/volumes/media",
			got:  &volumeResponse{},
			want: &volumeResponse{Name: "media", Driver: "local", Scope: "local", Labels: map[string]string{}},
		},
		{
			name: "networks sorted by name",
			path: "/networks",
			got:  &[]networkResponse{},
			want: &[]networkResponse{
				{ID: "n1", Name: "back", Driver: "bridge", Scope: "local", CreatedAt: &created, IPAM: []ipamResponse{{Subnet: "172.18.0.0/16", Gateway: "172.18.0.1"}}, Labels: map[string]string{}},
				{ID: "n2", Name: "front", Driver: "bridge", Scope: "local", IPAM: []ipamResponse{}, Labels: map[string]string{}},
			},
		},
		{
			name: "network",
			path: "/networks/front",
			got:  &networkDetailResponse{},
			want: &networkDetailResponse{
				networkResponse: networkResponse{ID: "n2", Name: "front", Driver: "bridge", IPAM: []ipamResponse{}, Labels: map[string]string{}},
				Options:         map[string]string{"com.docker.network.bridge.name": "br-front"},
				Containers:      []networkContainerResponse{{ID: "abc123", Name: "web", IPv4Address: "172.18.0.2/16"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := fmt.Sprintf("/api/v1/environments/%d%s", et.ids["local"], tt.path)
			rec := do(t, et.h, request{method: "GET", path: path, cookie: et.cookie})
			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d (%s)", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Errorf("body leaks volume options: %s", rec.Body)
			}
			if err := json.Unmarshal(rec.Body.Bytes(), tt.got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", tt.got, tt.want)
			}
		})
	}
}

func TestResourceErrors(t *testing.T) {
	t.Parallel()
	et := newResourceTest(t)
	env := func(name, path string) string { return fmt.Sprintf("/api/v1/environments/%d%s", et.ids[name], path) }
	tests := []struct {
		name     string
		path     string
		wantCode int
		wantErr  string
	}{
		{name: "unknown environment", path: "/api/v1/environments/999/containers", wantCode: 404, wantErr: "environment not found"},
		{name: "offline environment", path: env("down", "/images"), wantCode: 503, wantErr: "the environment is offline"},
		{name: "daemon error", path: env("broken", "/volumes"), wantCode: 502},
		{name: "offline inspect", path: env("down", "/networks/front"), wantCode: 503},
		{name: "unknown state", path: env("local", "/containers?state=sleeping"), wantCode: 400},
		{name: "unknown container", path: env("local", "/containers/nope"), wantCode: 404, wantErr: "container not found"},
		{name: "unknown image", path: env("local", "/images/nope"), wantCode: 404, wantErr: "image not found"},
		{name: "unknown volume", path: env("local", "/volumes/nope"), wantCode: 404, wantErr: "volume not found"},
		{name: "unknown network", path: env("local", "/networks/nope"), wantCode: 404, wantErr: "network not found"},
		{name: "container id with a slash", path: env("local", "/containers/..%2Fimages%2Fjson"), wantCode: 404, wantErr: "container not found"},
		{name: "image ref with dots", path: env("local", "/images/..%2F..%2Fcontainers"), wantCode: 404, wantErr: "image not found"},
		{name: "volume id with a space", path: env("local", "/volumes/a%20b"), wantCode: 404, wantErr: "volume not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := do(t, et.h, request{method: "GET", path: tt.path, cookie: et.cookie})
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
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
		})
	}

	if rec := do(t, et.h, request{method: "GET", path: env("local", "/containers")}); rec.Code != http.StatusUnauthorized {
		t.Errorf("without session: code = %d, want 401", rec.Code)
	}
}

func ptr[T any](v T) *T { return &v }
