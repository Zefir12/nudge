package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

// oldWeb is a running container to recreate: an image VOLUME at /cache with an anonymous
// volume, a named volume, a published port and two networks.
func oldWeb() container.InspectResponse {
	port := network.MustParsePort("80/tcp")
	return container.InspectResponse{
		ID: "abc123def4567890", Name: "/web", Image: "sha256:img1",
		State: &container.State{Status: container.StateRunning, Running: true},
		Config: &container.Config{
			Hostname: "abc123def456", Image: "nginx:1.29", Env: []string{"PATH=/bin", "TOKEN=hunter22"},
			Cmd: []string{"nginx"}, Labels: map[string]string{"app": "web"},
			Volumes: map[string]struct{}{"/cache": {}}, ExposedPorts: network.PortSet{port: {}},
		},
		HostConfig: &container.HostConfig{
			Binds:         []string{"data:/data"},
			PortBindings:  network.PortMap{port: {{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: "8080"}}},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			NetworkMode:   "front", CapAdd: []string{"NET_ADMIN"},
		},
		Mounts: []container.MountPoint{
			{Type: mount.TypeVolume, Name: "data", Destination: "/data", RW: true},
			{Type: mount.TypeVolume, Name: "f00anon", Destination: "/cache", RW: true},
		},
		NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
			"front": {Aliases: []string{"www"}, NetworkID: "n2", IPAddress: netip.MustParseAddr("172.18.0.2")},
			"back":  {NetworkID: "n1"},
		}},
	}
}

func TestCreateContainerValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want map[string]string
	}{
		{"no image", `{}`, map[string]string{"image": "required"}},
		{"bad fields", `{"image":"nginx","name":"x","env":["=1"],"ports":[{"container_port":0,"host_port":70000,"protocol":"icmp","host_ip":"nope"}],
			"mounts":[{"type":"tmpfs","target":"rel"},{"type":"bind","source":"rel","target":"/x"},{"type":"volume","source":"a/b","target":"/"}],
			"networks":["a b"],"restart_policy":"sometimes","labels":{"":"x"}}`, map[string]string{
			"name":  "2 or more letters, digits, '_', '.' or '-', starting with a letter or digit",
			"env.0": "must be NAME=value", "ports.0.container_port": "must be 1 to 65535",
			"ports.0.host_port": "must be 0 (any free port) to 65535", "ports.0.protocol": "must be tcp, udp or sctp",
			"ports.0.host_ip": "must be an IP address", "mounts.0.type": "must be volume or bind",
			"mounts.0.target": "must be an absolute path in the container, not /", "mounts.1.source": "must be an absolute path on the host",
			"mounts.2.source": "must be a volume name, or empty for a new anonymous volume", "mounts.2.target": "must be an absolute path in the container, not /",
			"networks.0": "must be a network name", "restart_policy": "must be no, always, unless-stopped or on-failure",
			"labels": "label names must not be empty",
		}},
		{"bad image", `{"image":"ghcr.io//x"}`, map[string]string{"image": "must be a reference such as nginx:1.29, or an image ID"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &dockertest.Fake{}
			et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
			path := fmt.Sprintf("/api/v1/environments/%d/containers", et.ids["local"])
			rec := do(t, et.h, request{method: "POST", path: path, body: tt.body, cookie: et.cookie})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400 (%s)", rec.Code, rec.Body)
			}
			var body fieldsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error != "invalid container settings" || !reflect.DeepEqual(body.Fields, tt.want) {
				t.Errorf("body = %+v, want fields %v", body, tt.want)
			}
			if len(fake.Actions()) != 0 {
				t.Errorf("actions = %+v, want none", fake.Actions())
			}
		})
	}
}

func TestCreateContainer(t *testing.T) {
	t.Parallel()
	body := `{"image":"nginx:1.29","name":"web","command":["nginx","-g","daemon off;"],"env":["TOKEN=hunter22"],
		"ports":[{"host_port":8080,"container_port":80},{"host_ip":"127.0.0.1","container_port":53,"protocol":"udp"}],
		"mounts":[{"type":"volume","source":"data","target":"/data/"},{"type":"bind","source":"/srv/conf","target":"/etc/nginx","read_only":true}],
		"networks":["front","back"],"restart_policy":"always","labels":{"app":"web"},"start":true}`
	p80, p53 := network.MustParsePort("80/tcp"), network.MustParsePort("53/udp")
	wantCreate := client.ContainerCreateOptions{
		Name: "web",
		Config: &container.Config{
			Image: "nginx:1.29", Cmd: []string{"nginx", "-g", "daemon off;"}, Env: []string{"TOKEN=hunter22"},
			Labels: map[string]string{"app": "web"}, ExposedPorts: network.PortSet{p80: {}, p53: {}},
		},
		HostConfig: &container.HostConfig{
			PortBindings: network.PortMap{p80: {{HostPort: "8080"}}, p53: {{HostIP: netip.MustParseAddr("127.0.0.1")}}},
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: "data", Target: "/data"},
				{Type: mount.TypeBind, Source: "/srv/conf", Target: "/etc/nginx", ReadOnly: true},
			},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyAlways}, NetworkMode: "front",
		},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{"front": {}, "back": {}}},
	}
	tests := []struct {
		name     string
		body     string
		fail     func(a dockertest.Action) error
		wantCode int
		wantBody string
		want     []dockertest.Action
	}{
		{name: "create and start", body: body, wantCode: 201, wantBody: `{"id":"new-1","warnings":[]}`,
			want: []dockertest.Action{{Name: "create", ID: "web", Options: wantCreate}, {Name: "start", ID: "new-1", Options: client.ContainerStartOptions{}}}},
		{name: "create only", body: `{"image":"busybox"}`, wantCode: 201, wantBody: `{"id":"new-1","warnings":[]}`,
			want: []dockertest.Action{{Name: "create", Options: client.ContainerCreateOptions{
				Config: &container.Config{Image: "busybox"}, HostConfig: &container.HostConfig{PortBindings: network.PortMap{}}, NetworkingConfig: &network.NetworkingConfig{},
			}}}},
		{name: "start fails", body: body, wantCode: 400,
			fail: func(a dockertest.Action) error {
				if a.Name == "start" {
					return errors.New("Error response from daemon: driver failed: TOKEN=hunter22: Bind for 0.0.0.0:8080 failed: port is already allocated")
				}
				return nil
			},
			wantBody: `{"error":"the container could not be started (it was removed again): driver failed: TOKEN=********: Bind for 0.0.0.0:8080 failed: port is already allocated"}`,
			want:     []dockertest.Action{{Name: "create", ID: "web", Options: wantCreate}, {Name: "remove", ID: "new-1", Options: client.ContainerRemoveOptions{Force: true}}}},
		{name: "name taken", body: body, wantCode: 409,
			fail: func(a dockertest.Action) error {
				if a.Name == "create" {
					return fmt.Errorf(`Conflict. The container name "/web" is already in use: %w`, cerrdefs.ErrConflict)
				}
				return nil
			},
			wantBody: `{"error":"the container could not be created: Conflict. The container name \"/web\" is already in use: conflict"}`},
		{name: "no such image", body: `{"image":"nope:1"}`, wantCode: 400,
			fail: func(a dockertest.Action) error {
				if a.Name == "create" {
					return fmt.Errorf("No such image: nope:1: %w", cerrdefs.ErrNotFound)
				}
				return nil
			},
			wantBody: `{"error":"the container could not be created: No such image: nope:1: not found"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &dockertest.Fake{Resources: dockertest.Resources{FailAction: tt.fail}}
			et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
			path := fmt.Sprintf("/api/v1/environments/%d/containers", et.ids["local"])
			rec := do(t, et.h, request{method: "POST", path: path, body: tt.body, cookie: et.cookie})
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
				t.Errorf("body = %s\nwant %s", got, tt.wantBody)
			}
			if got := fake.Actions(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("actions = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestGetContainerSpec(t *testing.T) {
	t.Parallel()
	fake := &dockertest.Fake{Resources: dockertest.Resources{ContainerDetails: map[string]container.InspectResponse{"web": oldWeb()}}}
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
	base := fmt.Sprintf("/api/v1/environments/%d/containers", et.ids["local"])
	rec := do(t, et.h, request{method: "GET", path: base + "/web/spec", cookie: et.cookie})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", rec.Code, rec.Body)
	}
	want := `{"image":"nginx:1.29","name":"web","command":["nginx"],"env":["PATH=/bin","TOKEN=hunter22"],` +
		`"ports":[{"host_ip":"","host_port":8080,"container_port":80,"protocol":"tcp"}],` +
		`"mounts":[{"type":"volume","source":"data","target":"/data","read_only":false},{"type":"volume","source":"f00anon","target":"/cache","read_only":false}],` +
		`"networks":["back","front"],"restart_policy":"unless-stopped","labels":{"app":"web"}}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body = %s\nwant %s", got, want)
	}
	if rec := do(t, et.h, request{method: "GET", path: base + "/nope/spec", cookie: et.cookie}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown container: code = %d, want 404", rec.Code)
	}
}

func TestRecreateContainer(t *testing.T) {
	t.Parallel()
	old := oldWeb()
	// The same settings without the hostname, the anonymous volume mounted again and the
	// endpoints' addresses left to Docker.
	sameCfg := *old.Config
	sameCfg.Hostname = ""
	sameHC := *old.HostConfig
	sameHC.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: "f00anon", Target: "/cache"}}
	sameNC := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{"front": {Aliases: []string{"www"}}, "back": {}}}
	same := client.ContainerCreateOptions{Name: "web", Config: &sameCfg, HostConfig: &sameHC, NetworkingConfig: sameNC}

	// A spec that changes the env and drops the network "back".
	changedCfg := sameCfg
	changedCfg.Env = []string{"TOKEN=newsecret1"}
	changedHC := sameHC
	// The unchanged volume stays a bind (keeping its options).
	changedHC.Binds = []string{"data:/data"}
	changedHC.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: "f00anon", Target: "/cache"}}
	changedHC.PortBindings = network.PortMap{network.MustParsePort("80/tcp"): {{HostPort: "8080"}}}
	changed := client.ContainerCreateOptions{Name: "web", Config: &changedCfg, HostConfig: &changedHC,
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{"front": {Aliases: []string{"www"}}}}}
	spec := `{"image":"nginx:1.29","name":"web","command":["nginx"],"env":["TOKEN=newsecret1"],"ports":[{"host_port":8080,"container_port":80}],` +
		`"mounts":[{"type":"volume","source":"data","target":"/data"}],"networks":["front"],"restart_policy":"unless-stopped","labels":{"app":"web"}}`

	stop := dockertest.Action{Name: "stop", ID: old.ID, Options: client.ContainerStopOptions{}}
	aside := dockertest.Action{Name: "rename", ID: old.ID, Options: client.ContainerRenameOptions{NewName: "web-nudge-old"}}
	back := dockertest.Action{Name: "rename", ID: old.ID, Options: client.ContainerRenameOptions{NewName: "web"}}
	startNew := dockertest.Action{Name: "start", ID: "new-1", Options: client.ContainerStartOptions{}}
	startOld := dockertest.Action{Name: "start", ID: old.ID, Options: client.ContainerStartOptions{}}
	removeOld := dockertest.Action{Name: "remove", ID: old.ID, Options: client.ContainerRemoveOptions{Force: true}}
	removeNew := dockertest.Action{Name: "remove", ID: "new-1", Options: client.ContainerRemoveOptions{Force: true}}
	failNewStart := func(a dockertest.Action) error {
		if a.Name == "start" && a.ID == "new-1" {
			return errors.New("Error response from daemon: oci runtime: TOKEN=hunter22 invalid")
		}
		return nil
	}
	tests := []struct {
		name     string
		body     string
		stopped  bool
		fail     func(a dockertest.Action) error
		wantCode int
		wantBody string
		want     []dockertest.Action
	}{
		{name: "same settings", body: `{}`, wantCode: 200, wantBody: `{"id":"new-1","warnings":[]}`,
			want: []dockertest.Action{stop, aside, {Name: "create", ID: "web", Options: same}, startNew, removeOld}},
		{name: "pull first", body: `{"pull":true}`, wantCode: 200, wantBody: `{"id":"new-1","warnings":[]}`,
			want: []dockertest.Action{{Name: "image-pull", ID: "nginx:1.29", Options: client.ImagePullOptions{}}, stop, aside, {Name: "create", ID: "web", Options: same}, startNew, removeOld}},
		{name: "changed settings", body: `{"spec":` + spec + `}`, wantCode: 200, wantBody: `{"id":"new-1","warnings":[]}`,
			want: []dockertest.Action{stop, aside, {Name: "create", ID: "web", Options: changed}, startNew, removeOld}},
		{name: "stopped container stays stopped", body: `{}`, stopped: true, wantCode: 200, wantBody: `{"id":"new-1","warnings":[]}`,
			want: []dockertest.Action{aside, {Name: "create", ID: "web", Options: same}, removeOld}},
		{name: "start fails: old one restored", body: `{}`, fail: failNewStart, wantCode: 400,
			wantBody: `{"error":"the container could not be started (the old one was restored): oci runtime: TOKEN=******** invalid"}`,
			want:     []dockertest.Action{stop, aside, {Name: "create", ID: "web", Options: same}, removeNew, back, startOld}},
		{name: "create fails: old one restored", body: `{}`, wantCode: 400,
			fail: func(a dockertest.Action) error {
				if a.Name == "create" {
					return fmt.Errorf("invalid mount: %w", cerrdefs.ErrInvalidArgument)
				}
				return nil
			},
			wantBody: `{"error":"the container could not be created (the old one was restored): invalid mount: invalid argument"}`,
			want:     []dockertest.Action{stop, aside, back, startOld}},
		{name: "restore fails too", body: `{}`, wantCode: 400,
			fail: func(a dockertest.Action) error {
				if err := failNewStart(a); err != nil {
					return err
				}
				if a.Name == "rename" && a.Options == (client.ContainerRenameOptions{NewName: "web"}) {
					return errors.New("daemon busy")
				}
				return nil
			},
			wantBody: `{"error":"the container could not be started (and restoring the old one failed: it may still be stopped or named web-nudge-old, check it by hand): oci runtime: TOKEN=******** invalid"}`,
			want:     []dockertest.Action{stop, aside, {Name: "create", ID: "web", Options: same}, removeNew, startOld}},
		{name: "old name taken", body: `{}`, wantCode: 409,
			fail: func(a dockertest.Action) error {
				if a.Name == "rename" && a.Options == (client.ContainerRenameOptions{NewName: "web-nudge-old"}) {
					return cerrdefs.ErrConflict
				}
				return nil
			},
			wantBody: `{"error":"a container named web-nudge-old exists (left by an earlier recreate?): remove or rename it first"}`,
			want:     []dockertest.Action{stop, back, startOld}},
		{name: "stop fails: old one started again", body: `{}`, wantCode: 502,
			fail: func(a dockertest.Action) error {
				if a.Name == "stop" {
					return errors.New("secret-detail")
				}
				return nil
			},
			wantBody: `{"error":"the environment's Docker host returned an error"}`,
			want:     []dockertest.Action{startOld}},
		{name: "invalid spec", body: `{"spec":{"image":""}}`, wantCode: 400,
			wantBody: `{"error":"invalid container settings","fields":{"image":"required"}}`},
		{name: "pull by ID", body: `{"pull":true,"spec":{"image":"sha256:0123456789abcdef0123456789abcdef"}}`, wantCode: 400,
			wantBody: `{"error":"the image is an ID, not a reference such as nginx:1.29, so it can't be pulled"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ct := oldWeb()
			if tt.stopped {
				ct.State = &container.State{Status: container.StateExited}
			}
			fake := &dockertest.Fake{Resources: dockertest.Resources{
				ContainerDetails: map[string]container.InspectResponse{old.ID: ct},
				FailAction:       tt.fail,
			}}
			et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
			path := fmt.Sprintf("/api/v1/environments/%d/containers/%s/recreate", et.ids["local"], old.ID)
			rec := do(t, et.h, request{method: "POST", path: path, body: tt.body, cookie: et.cookie})
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
				t.Errorf("body = %s\nwant %s", got, tt.wantBody)
			}
			if got := fake.Actions(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("actions:\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestRecreateUnknownContainer(t *testing.T) {
	t.Parallel()
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": {}})
	path := fmt.Sprintf("/api/v1/environments/%d/containers/nope/recreate", et.ids["local"])
	if rec := do(t, et.h, request{method: "POST", path: path, body: `{}`, cookie: et.cookie}); rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404 (%s)", rec.Code, rec.Body)
	}
	for _, req := range []request{
		{method: "POST", path: path, body: `{}`},
		{method: "POST", path: fmt.Sprintf("/api/v1/environments/%d/containers", et.ids["local"]), body: `{"image":"nginx"}`},
		{method: "GET", path: fmt.Sprintf("/api/v1/environments/%d/containers/nope/spec", et.ids["local"])},
	} {
		if rec := do(t, et.h, req); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without session: code = %d, want 401", req.method, req.path, rec.Code)
		}
	}
}

// newRecreateTest serves one container, ct, with the image details given.
func newRecreateTest(t *testing.T, ct container.InspectResponse, images map[string]image.InspectResponse, fail func(dockertest.Action) error) (envTest, *dockertest.Fake, string) {
	t.Helper()
	fake := &dockertest.Fake{Resources: dockertest.Resources{
		ContainerDetails: map[string]container.InspectResponse{ct.ID: ct}, ImageDetails: images, FailAction: fail,
	}}
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": fake})
	return et, fake, fmt.Sprintf("/api/v1/environments/%d/containers/%s", et.ids["local"], ct.ID)
}

func createOptions(t *testing.T, fake *dockertest.Fake) client.ContainerCreateOptions {
	t.Helper()
	for _, a := range fake.Actions() {
		if a.Name == "create" {
			return a.Options.(client.ContainerCreateOptions)
		}
	}
	t.Fatalf("no create in %+v", fake.Actions())
	return client.ContainerCreateOptions{}
}

func TestRecreateLeavesImageDefaultsToTheImage(t *testing.T) {
	t.Parallel()
	ct := oldWeb()
	ct.Config.Labels["org.opencontainers.image.version"] = "1.29"
	images := map[string]image.InspectResponse{"sha256:img1": {Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{
		Env: []string{"PATH=/bin"}, Cmd: []string{"nginx"}, Volumes: map[string]struct{}{"/cache": {}},
		ExposedPorts: map[string]struct{}{"80/tcp": {}}, Labels: map[string]string{"org.opencontainers.image.version": "1.29"},
	}}}}
	et, fake, base := newRecreateTest(t, ct, images, nil)

	rec := do(t, et.h, request{method: "GET", path: base + "/spec", cookie: et.cookie})
	var sp containerSpec
	if err := json.Unmarshal(rec.Body.Bytes(), &sp); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sp.Env, []string{"TOKEN=hunter22"}) || len(sp.Command) != 0 || !reflect.DeepEqual(sp.Labels, map[string]string{"app": "web"}) {
		t.Errorf("spec = %+v, want only the container's own env and labels, no command", sp)
	}

	if rec := do(t, et.h, request{method: "POST", path: base + "/recreate", body: `{"pull":true}`, cookie: et.cookie}); rec.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", rec.Code, rec.Body)
	}
	cfg := createOptions(t, fake).Config
	if !reflect.DeepEqual(cfg.Env, []string{"TOKEN=hunter22"}) || cfg.Cmd != nil || len(cfg.Volumes) != 0 || len(cfg.ExposedPorts) != 0 ||
		!reflect.DeepEqual(cfg.Labels, map[string]string{"app": "web"}) {
		t.Errorf("config = %+v, want the image's defaults left out", cfg)
	}
	if hc := createOptions(t, fake).HostConfig; len(hc.Mounts) != 1 || hc.Mounts[0].Source != "f00anon" {
		t.Errorf("mounts = %+v, want the anonymous volume kept", hc.Mounts)
	}
}

func TestRecreateKeepsMounts(t *testing.T) {
	t.Parallel()
	ct := oldWeb()
	ct.HostConfig.Binds = []string{"data:/data", "/srv/conf:/etc/nginx:ro,z"}
	ct.HostConfig.Mounts = []mount.Mount{
		{Type: mount.TypeVolume, Target: "/scratch"},
		{Type: mount.TypeVolume, Source: "logs", Target: "/logs", VolumeOptions: &mount.VolumeOptions{NoCopy: true}},
	}
	ct.Mounts = append(ct.Mounts,
		container.MountPoint{Type: mount.TypeVolume, Name: "beefanon", Destination: "/scratch", RW: true},
		container.MountPoint{Type: mount.TypeVolume, Name: "logs", Destination: "/logs", RW: true},
	)
	wantMounts := []mount.Mount{
		{Type: mount.TypeVolume, Source: "beefanon", Target: "/scratch"},
		{Type: mount.TypeVolume, Source: "logs", Target: "/logs", VolumeOptions: &mount.VolumeOptions{NoCopy: true}},
		{Type: mount.TypeVolume, Source: "f00anon", Target: "/cache"},
	}
	tests := []struct {
		name      string
		body      string
		wantBinds []string
		wantMount []mount.Mount
	}{
		{"same settings", `{}`, []string{"data:/data", "/srv/conf:/etc/nginx:ro,z"}, wantMounts},
		{"spec makes the bind writable", `{"spec":{"image":"nginx:1.29","mounts":[` +
			`{"type":"volume","source":"data","target":"/data"},{"type":"bind","source":"/srv/conf","target":"/etc/nginx"},` +
			`{"type":"volume","target":"/scratch"},{"type":"volume","source":"logs","target":"/logs","read_only":true}]}}`,
			[]string{"data:/data", "/srv/conf:/etc/nginx:z"},
			[]mount.Mount{
				{Type: mount.TypeVolume, Source: "beefanon", Target: "/scratch"},
				{Type: mount.TypeVolume, Source: "logs", Target: "/logs", ReadOnly: true, VolumeOptions: &mount.VolumeOptions{NoCopy: true}},
				{Type: mount.TypeVolume, Source: "f00anon", Target: "/cache"},
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			et, fake, base := newRecreateTest(t, ct, nil, nil)
			if rec := do(t, et.h, request{method: "POST", path: base + "/recreate", body: tt.body, cookie: et.cookie}); rec.Code != http.StatusOK {
				t.Fatalf("code = %d (%s)", rec.Code, rec.Body)
			}
			hc := createOptions(t, fake).HostConfig
			if !reflect.DeepEqual(hc.Binds, tt.wantBinds) {
				t.Errorf("binds = %q, want %q", hc.Binds, tt.wantBinds)
			}
			if !reflect.DeepEqual(hc.Mounts, tt.wantMount) {
				t.Errorf("mounts = %+v\nwant %+v", hc.Mounts, tt.wantMount)
			}
		})
	}
}

func TestRecreateOneAtATime(t *testing.T) {
	t.Parallel()
	var et envTest
	var base string
	var second int
	fail := func(a dockertest.Action) error {
		if a.Name == "create" && second == 0 {
			rec := do(t, et.h, request{method: "POST", path: base + "/recreate", body: `{}`, cookie: et.cookie})
			second = rec.Code
			if !strings.Contains(rec.Body.String(), "this container is already being recreated") {
				t.Errorf("second recreate: %s", rec.Body)
			}
		}
		return nil
	}
	et, _, base = newRecreateTest(t, oldWeb(), nil, fail)
	if rec := do(t, et.h, request{method: "POST", path: base + "/recreate", body: `{}`, cookie: et.cookie}); rec.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", rec.Code, rec.Body)
	}
	if second != http.StatusConflict {
		t.Errorf("second recreate during the first: code = %d, want 409", second)
	}
}
