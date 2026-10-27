package api

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func TestBuildTenant(t *testing.T) {
	t.Parallel()
	sp := tenantSpec{
		Image: "nginx:1.29", Name: "web", Command: []string{"nginx", "-g", "daemon off;"}, Entrypoint: []string{"/entry"},
		Env: []string{"A=1"}, CPUsMilli: 500, MemoryBytes: 256 << 20,
		Ports:         []tenantPort{{HostPort: 8080, ContainerPort: 80}, {ContainerPort: 53, Protocol: "udp"}},
		Mounts:        []tenantMount{{Volume: "u7-data", Target: "/data/", ReadOnly: true}},
		Networks:      []string{"u7-front", "u7-back"},
		RestartPolicy: "unless-stopped",
		Healthcheck:   &tenantHealth{Command: []string{"/bin/check", "-q"}, IntervalSeconds: 30, TimeoutSeconds: 5, StartPeriodSeconds: 10, Retries: 3},
		// A reserved label can't get past validate; the builder drops it all the same.
		Labels: map[string]string{"app": "web", ownerLabel: "forged"},
	}
	cfg, hc, nc := buildTenant(&sp, "key7")

	initProcess, pids := true, int64(512)
	http80, _ := network.ParsePort("80/tcp")
	dns53, _ := network.ParsePort("53/udp")
	wantHC := &container.HostConfig{
		CapDrop:       []string{"NET_RAW"},
		SecurityOpt:   []string{"no-new-privileges:true"},
		Init:          &initProcess,
		IpcMode:       container.IPCModePrivate,
		NetworkMode:   "u7-front",
		LogConfig:     container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		Resources: container.Resources{
			NanoCPUs: 500_000_000, Memory: 256 << 20, MemorySwap: 256 << 20, PidsLimit: &pids,
		},
		PortBindings: network.PortMap{http80: {{HostPort: "8080"}}, dns53: {{}}},
		Mounts:       []mount.Mount{{Type: mount.TypeVolume, Source: "u7-data", Target: "/data", ReadOnly: true}},
	}
	if !reflect.DeepEqual(hc, wantHC) {
		t.Errorf("host config =\n%+v\nwant\n%+v", hc, wantHC)
	}
	// Spelled out, so a change to the golden value above can't quietly allow them.
	if hc.Privileged || len(hc.Binds) > 0 || len(hc.CapAdd) > 0 || len(hc.Devices) > 0 || hc.PidMode != "" ||
		hc.UsernsMode != "" || hc.UTSMode != "" || len(hc.VolumesFrom) > 0 || hc.Runtime != "" || hc.CgroupParent != "" {
		t.Errorf("host config grants more than the builder should: %+v", hc)
	}

	wantCfg := &container.Config{
		Image: "nginx:1.29", Cmd: []string{"nginx", "-g", "daemon off;"}, Entrypoint: []string{"/entry"}, Env: []string{"A=1"},
		Labels:       map[string]string{"app": "web", ownerLabel: "key7"},
		ExposedPorts: network.PortSet{http80: {}, dns53: {}},
		Healthcheck: &container.HealthConfig{
			Test: []string{"CMD", "/bin/check", "-q"}, Interval: 30 * time.Second, Timeout: 5 * time.Second,
			StartPeriod: 10 * time.Second, Retries: 3,
		},
	}
	if !reflect.DeepEqual(cfg, wantCfg) {
		t.Errorf("config =\n%+v\nwant\n%+v", cfg, wantCfg)
	}
	if len(nc.EndpointsConfig) != 2 || nc.EndpointsConfig["u7-front"] == nil || nc.EndpointsConfig["u7-back"] == nil {
		t.Errorf("endpoints = %+v", nc.EndpointsConfig)
	}

	// Read back, the container gives the same spec.
	back := toTenantSpec(container.InspectResponse{
		Name: "/u7-web", Config: cfg, HostConfig: hc,
		Mounts:          []container.MountPoint{{Type: mount.TypeVolume, Name: "u7-data", Destination: "/data"}},
		NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{"u7-back": {}, "u7-front": {}}},
	}, nil, "u7-")
	sp.Labels = map[string]string{"app": "web"}
	sp.Ports = []tenantPort{{ContainerPort: 53, Protocol: "udp"}, {HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	sp.Mounts[0].Target = "/data"
	if !reflect.DeepEqual(back, sp) {
		t.Errorf("read back =\n%+v\nwant\n%+v", back, sp)
	}
}

// tenantTest is newOwnTest with bob's volume "mine" and network "mine", and images: nginx:1
// (plain), postgres:17 (a VOLUME) and evil:1 (sets the owner label).
func newTenantTest(t *testing.T) ownTest {
	t.Helper()
	ot := newOwnTest(t)
	ot.fake.ImageDetails["postgres:17"] = image.InspectResponse{ID: "sha256:pg", Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{
		Volumes: map[string]struct{}{"/var/lib/postgresql/data": {}},
	}}}
	ot.fake.ImageDetails["evil:1"] = image.InspectResponse{ID: "sha256:evil", Config: &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{
		Labels: map[string]string{ownerLabel: ot.alice},
	}}}
	ot.fake.Volumes = append(ot.fake.Volumes, volume.Volume{Name: "pgdata", Driver: "local", Labels: map[string]string{ownerLabel: ot.bobKey}})
	return ot
}

// created returns the options of the containers the fake created since action before.
func created(fake *dockertest.Fake, before int) []client.ContainerCreateOptions {
	var out []client.ContainerCreateOptions
	for _, a := range fake.Actions()[before:] {
		if a.Name == "create" {
			out = append(out, a.Options.(client.ContainerCreateOptions))
		}
	}
	return out
}

// checkHardened checks a container bob's request created: built by the tenant builder.
func checkHardened(t *testing.T, ot ownTest, opts client.ContainerCreateOptions) {
	t.Helper()
	hc := opts.HostConfig
	if hc.Privileged || len(hc.Binds) > 0 || len(hc.CapAdd) > 0 || !slices.Equal(hc.CapDrop, []string{"NET_RAW"}) ||
		hc.PidsLimit == nil || *hc.PidsLimit != tenantPidsLimit || hc.Init == nil || !*hc.Init ||
		!slices.Contains(hc.SecurityOpt, "no-new-privileges:true") || hc.NetworkMode.IsHost() {
		t.Errorf("host config not hardened: %+v", hc)
	}
	if opts.Config.Labels[ownerLabel] != ot.bobKey {
		t.Errorf("owner label = %q, want bob's", opts.Config.Labels[ownerLabel])
	}
}

const tenantBase = `"image":"nginx:1","name":"web","cpus_milli":500,"memory_bytes":268435456`

func TestCreateTenantContainer(t *testing.T) {
	t.Parallel()
	ot := newTenantTest(t)
	path := ot.path(ot.local, "/user-containers")
	before := len(ot.fake.Actions())
	rec := do(t, ot.h, request{method: "POST", path: path, cookie: ot.bob, body: `{` + tenantBase +
		`,"ports":[{"container_port":80}],"mounts":[{"volume":"mine","target":"/srv"}],"labels":{"app":"web"},"start":true}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	cs := created(ot.fake, before)
	if len(cs) != 1 {
		t.Fatalf("created %d containers", len(cs))
	}
	opts := cs[0]
	checkHardened(t, ot, opts)
	if want := userPrefix(ot.bobID) + "web"; opts.Name != want {
		t.Errorf("name = %q, want %q", opts.Name, want)
	}
	if want := defaultNetwork(ot.bobID); string(opts.HostConfig.NetworkMode) != want {
		t.Errorf("network = %q, want bob's default %q", opts.HostConfig.NetworkMode, want)
	}
	port, _ := network.ParsePort("80/tcp")
	if b := opts.HostConfig.PortBindings[port]; len(b) != 1 || b[0].HostPort != "8000" {
		t.Errorf("port bindings = %+v, want the first of bob's range", opts.HostConfig.PortBindings)
	}
	var pulled, started bool
	for _, a := range ot.fake.Actions()[before:] {
		switch a.Name {
		case "image-pull":
			pulled = true
			if auth := a.Options.(client.ImagePullOptions).RegistryAuth; auth != "" {
				t.Errorf("user's pull sent registry credentials %q", auth)
			}
		case "start":
			started = true
		}
	}
	if !pulled || !started {
		t.Errorf("pulled %v, started %v; want both", pulled, started)
	}

	// The settings read back for the form.
	rec = do(t, ot.h, request{method: "GET", path: ot.path(ot.local, "/user-containers/mine/spec"), cookie: ot.bob})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cpus_milli":500`) {
		t.Errorf("spec = %d %s", rec.Code, rec.Body)
	}
}

func TestCreateTenantRefusals(t *testing.T) {
	t.Parallel()
	ot := newTenantTest(t)
	path := ot.path(ot.local, "/user-containers")
	tests := []struct {
		name, body string
		code       int
		want       string
	}{
		{"privileged", `,"privileged":true`, 400, `unknown field \"privileged\"`},
		{"cap_add", `,"cap_add":["SYS_ADMIN"]`, 400, `unknown field \"cap_add\"`},
		{"binds", `,"binds":["/:/host"]`, 400, `unknown field \"binds\"`},
		{"network_mode", `,"network_mode":"host"`, 400, `unknown field \"network_mode\"`},
		{"pid", `,"pid":"host"`, 400, `unknown field \"pid\"`},
		{"devices", `,"devices":["/dev/sda"]`, 400, `unknown field \"devices\"`},
		{"security_opt", `,"security_opt":["seccomp=unconfined"]`, 400, `unknown field \"security_opt\"`},
		{"runtime", `,"runtime":"runc"`, 400, `unknown field \"runtime\"`},
		{"cgroup_parent", `,"cgroup_parent":"/"`, 400, `unknown field \"cgroup_parent\"`},
		{"volumes_from", `,"volumes_from":["admins"]`, 400, `unknown field \"volumes_from\"`},
		{"bind mount", `,"mounts":[{"type":"bind","volume":"mine","target":"/host"}]`, 400, `unknown field \"type\"`},
		{"alice's volume", `,"mounts":[{"volume":"alices","target":"/d"}]`, 400, `"mounts.0.volume":"not one of your volumes"`},
		{"admins' volume", `,"mounts":[{"volume":"admins","target":"/d"}]`, 400, `"mounts.0.volume":"not one of your volumes"`},
		{"missing volume", `,"mounts":[{"volume":"nope","target":"/d"}]`, 400, `"mounts.0.volume":"not one of your volumes"`},
		{"alice's network", `,"networks":["alices"]`, 400, `"networks.0":"not one of your networks"`},
		{"network by ID", `,"networks":["mine","admins"]`, 400, `"networks.1":"not one of your networks"`},
		{"port outside range", `,"ports":[{"host_port":9500,"container_port":80}]`, 409, "outside your ports 8000-8999"},
		{"over allowance", `,"cpus_milli":5000`, 409, "over your allowance"},
		{"no memory limit", `,"memory_bytes":0`, 400, `"memory_bytes"`},
		{"reserved label", `,"labels":{"net.zefirlabs.nudge.owner":"x"}`, 400, `"labels":"labels starting with net.zefirlabs.nudge.`},
		{"image ID", `,"image":"sha256:` + strings.Repeat("a", 64) + `"`, 400, `"image"`},
		{"anonymous volume", `,"image":"postgres:17"`, 400, `/var/lib/postgresql/data: mount one of your volumes there`},
		{"image sets the owner label", `,"image":"evil:1"`, 400, "reserved for nudge"},
		{"no name", `,"name":""`, 400, `"name":"required"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(ot.fake.Actions())
			rec := do(t, ot.h, request{method: "POST", path: path, cookie: ot.bob, body: `{` + tenantBase + tt.body + `}`})
			if rec.Code != tt.code || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("= %d %s, want %d with %s", rec.Code, rec.Body, tt.code, tt.want)
			}
			if cs := created(ot.fake, before); len(cs) > 0 {
				t.Errorf("created %+v", cs)
			}
		})
	}

	// Covered by a volume of bob's, the image's VOLUME is fine.
	rec := do(t, ot.h, request{method: "POST", path: path, cookie: ot.bob, body: `{` + tenantBase +
		`,"image":"postgres:17","mounts":[{"volume":"pgdata","target":"/var/lib/postgresql/data"}]}`})
	if rec.Code != http.StatusCreated {
		t.Errorf("covered VOLUME = %d %s", rec.Code, rec.Body)
	}
}

// Every path that replaces bob's container rebuilds it through the builder, even when the
// container somehow carries settings the builder never makes.
func TestTenantReplacePaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(t *testing.T, ot ownTest) int
		code int
		// name of the new container; "" when none may be created.
		created string
	}{
		{"user update", func(t *testing.T, ot ownTest) int {
			return do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/containers/mine/update"), cookie: ot.bob}).Code
		}, http.StatusOK, "mine"},
		{"user recreate", func(t *testing.T, ot ownTest) int {
			return do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/user-containers/mine/recreate"), cookie: ot.bob,
				body: `{` + tenantBase + `}`}).Code
		}, http.StatusOK, "web"},
		{"user recreate with a refused field", func(t *testing.T, ot ownTest) int {
			return do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/user-containers/mine/recreate"), cookie: ot.bob,
				body: `{` + tenantBase + `,"privileged":true}`}).Code
		}, http.StatusBadRequest, ""},
		{"user recreate over allowance", func(t *testing.T, ot ownTest) int {
			return do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/user-containers/mine/recreate"), cookie: ot.bob,
				body: `{` + tenantBase + `,"memory_bytes":17179869184}`}).Code
		}, http.StatusConflict, ""},
		{"admin recreate", func(t *testing.T, ot ownTest) int {
			return do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/containers/mine/recreate"), cookie: ot.admin, body: `{}`}).Code
		}, http.StatusOK, "mine"},
		{"admin recreate with the admin spec", func(t *testing.T, ot ownTest) int {
			return do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/containers/mine/recreate"), cookie: ot.admin,
				body: `{"spec":{"image":"nginx:1","mounts":[{"type":"bind","source":"/","target":"/host"}]}}`}).Code
		}, http.StatusConflict, ""},
		{"user recreate with the admin spec", func(t *testing.T, ot ownTest) int {
			return do(t, ot.h, request{method: "POST", path: ot.path(ot.local, "/containers/mine/recreate"), cookie: ot.bob,
				body: `{"spec":{"image":"nginx:1"}}`}).Code
		}, http.StatusForbidden, ""},
		{"deploy webhook", func(t *testing.T, ot ownTest) int {
			hook := createHook(t, ot.h, ot.admin, ot.path(ot.local, "/webhooks"), `{"name":"ci","container":"mine"}`)
			rec := trigger(t, ot.h, hook.ID, "?wait=1", "", bearer(hook.Secret))
			if run := runOf(t, rec); run.Status != "updated" {
				t.Errorf("run = %+v", run)
			}
			return rec.Code
		}, http.StatusOK, "mine"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ot := newTenantTest(t)
			// Settings the builder never makes, and a newer image to update to.
			ct := ot.fake.ContainerDetails["mine"]
			ct.HostConfig = &container.HostConfig{
				Privileged: true, Binds: []string{"/:/host"}, CapAdd: []string{"SYS_ADMIN"}, NetworkMode: "host",
				Resources: container.Resources{NanoCPUs: 500_000_000, Memory: 256 << 20},
			}
			ot.fake.ContainerDetails["mine"] = ct
			ot.fake.ImageDetails["nginx:1"] = image.InspectResponse{ID: "sha256:newer", Config: &dockerspec.DockerOCIImageConfig{}}
			before := len(ot.fake.Actions())
			if code := tt.run(t, ot); code != tt.code {
				t.Errorf("code = %d, want %d", code, tt.code)
			}
			cs := created(ot.fake, before)
			if tt.created == "" {
				if len(cs) > 0 {
					t.Errorf("created %+v", cs)
				}
				return
			}
			if len(cs) != 1 {
				t.Fatalf("created %d containers", len(cs))
			}
			checkHardened(t, ot, cs[0])
			want := tt.created
			if want != "mine" {
				want = userPrefix(ot.bobID) + want
			}
			if cs[0].Name != want || string(cs[0].HostConfig.NetworkMode) != defaultNetwork(ot.bobID) {
				t.Errorf("created %s on %s, want %s on bob's default network", cs[0].Name, cs[0].HostConfig.NetworkMode, want)
			}
		})
	}
}

func TestTenantSpecValidate(t *testing.T) {
	t.Parallel()
	valid := func() tenantSpec {
		return tenantSpec{Image: "nginx:1", Name: "web", CPUsMilli: 100, MemoryBytes: 64 << 20}
	}
	tests := []struct {
		name  string
		edit  func(*tenantSpec)
		field string
	}{
		{"valid", func(*tenantSpec) {}, ""},
		{"no image", func(sp *tenantSpec) { sp.Image = " " }, "image"},
		{"bad name", func(sp *tenantSpec) { sp.Name = "-x" }, "name"},
		{"env", func(sp *tenantSpec) { sp.Env = []string{"=x"} }, "env.0"},
		{"no cpus", func(sp *tenantSpec) { sp.CPUsMilli = 0 }, "cpus_milli"},
		{"tiny memory", func(sp *tenantSpec) { sp.MemoryBytes = 1 << 20 }, "memory_bytes"},
		{"container port", func(sp *tenantSpec) { sp.Ports = []tenantPort{{ContainerPort: 0}} }, "ports.0.container_port"},
		{"protocol", func(sp *tenantSpec) { sp.Ports = []tenantPort{{ContainerPort: 1, Protocol: "sctp"}} }, "ports.0.protocol"},
		{"port twice", func(sp *tenantSpec) {
			sp.Ports = []tenantPort{{HostPort: 8000, ContainerPort: 1}, {HostPort: 8000, ContainerPort: 2, Protocol: "tcp"}}
		}, "ports.1.host_port"},
		{"mount target", func(sp *tenantSpec) { sp.Mounts = []tenantMount{{Volume: "v", Target: "/"}} }, "mounts.0.target"},
		{"mount twice", func(sp *tenantSpec) {
			sp.Mounts = []tenantMount{{Volume: "v", Target: "/d"}, {Volume: "w", Target: "/d/"}}
		}, "mounts.1.target"},
		{"mount volume", func(sp *tenantSpec) { sp.Mounts = []tenantMount{{Volume: "/etc", Target: "/d"}} }, "mounts.0.volume"},
		{"network twice", func(sp *tenantSpec) { sp.Networks = []string{"a", "a"} }, "networks.1"},
		{"restart", func(sp *tenantSpec) { sp.RestartPolicy = "sometimes" }, "restart_policy"},
		{"healthcheck", func(sp *tenantSpec) { sp.Healthcheck = &tenantHealth{} }, "healthcheck.command"},
		{"healthcheck interval", func(sp *tenantSpec) {
			sp.Healthcheck = &tenantHealth{Command: []string{"x"}, IntervalSeconds: -1}
		}, "healthcheck.interval_seconds"},
		{"empty label", func(sp *tenantSpec) { sp.Labels = map[string]string{"": "x"} }, "labels"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sp := valid()
			tt.edit(&sp)
			errs := sp.validate()
			if tt.field == "" && len(errs) > 0 || tt.field != "" && errs[tt.field] == "" {
				t.Errorf("errors = %v, want one for %q", errs, tt.field)
			}
		})
	}
}
